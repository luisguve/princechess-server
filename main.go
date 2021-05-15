// Copyright 2013 The Gorilla WebSocket Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	// "flag"
	"encoding/json"
	"log"
	"net/http"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
    "github.com/rs/cors"
	idGen "github.com/rs/xid"
	// "github.com/segmentio/ksuid"
)

const DEFAULT_USERNAME = "mistery"

// var port = flag.String("port", "8000", "http service address")

type router struct {
	rm           *roomMatcher
	wr           waitRooms
	m            *sync.Mutex
	store        *sessions.CookieStore
	count        int
	matches      map[string]match // map game ids to matches
	waiting1min  user // ids of users
	waiting3min  user
	waiting5min  user
	waiting10min user
	opp1min      chan match
	opp3min      chan match
	opp5min      chan match
	opp10min     chan match
	ldHub        *livedataHub
}

type inviteRoom struct {
	clock string
	host  user
	opp   chan match
}

// Rooms for invite links
type waitRooms struct {
	rooms1min  map[string]*inviteRoom
	rooms3min  map[string]*inviteRoom
	rooms5min  map[string]*inviteRoom
	rooms10min map[string]*inviteRoom
}

func newWaitRooms() waitRooms {
	return waitRooms{
		rooms1min:  make(map[string]*inviteRoom),
		rooms3min:  make(map[string]*inviteRoom),
		rooms5min:  make(map[string]*inviteRoom),
		rooms10min: make(map[string]*inviteRoom),
	}
}

type match struct {
	gameId string
	white  user
	black  user
}

type user struct {
	id       string
	username string
}

func (rout *router) makeRoom(m match) {
	rout.m.Lock()
	defer rout.m.Unlock()
	rout.count++
	rout.matches[m.gameId] = m
}

func (rout *router) newMatch(uid, username string, waiting *user, opp chan match) (playRoomId, color, oppUsername string) {
	deadline := time.NewTimer(5 * time.Second)
	rout.m.Lock()
	if waiting.id == "" {
		*waiting = user{
			id:       uid,
			username: username,
		}
		rout.m.Unlock()
		select {
		case match := <-opp:
			deadline.Stop()
			if match.gameId == "" {
				// game cancelled
				return
			}
			match.white = user{
				id: uid,
				username: username,
			}

			rout.makeRoom(match)
			playRoomId = match.gameId
			color = "white"
			oppUsername = match.black.username
		case <-deadline.C:
			rout.m.Lock()
			defer rout.m.Unlock()
			*waiting = user{}
			return
		}
	} else {
		if waiting.id == uid {
			// reset
			opp<- match{}
			*waiting = user{}
			rout.m.Unlock()
			return rout.newMatch(uid, username, waiting, opp)
		}
		playRoomId = idGen.New().String()
		opp<- match{
			gameId: playRoomId,
			black:  user{
				id: uid,
				username: username,
			},
		}
		oppUsername = waiting.username
		*waiting = user{}
		rout.m.Unlock()
		color = "black"
	}
	return
}

func (rout *router) handlePlay(w http.ResponseWriter, r *http.Request) {
	session, err := rout.store.Get(r, "sess")
	if err != nil {
		log.Printf("Get cookie error: %v", err)
	}
	uidBlob := session.Values["uid"]
	var (
		uid string
		ok bool
	)
	if uid, ok = uidBlob.(string); !ok {
		uid = idGen.New().String()
		session.Values["uid"] = uid
		if err := rout.store.Save(r, w, session); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	usernameBlob := session.Values["username"]
	var username string
	if username, ok = usernameBlob.(string); !ok {
		username = DEFAULT_USERNAME
	}
	vars := mux.Vars(r)
	if vars["clock"] == "" {
		http.Error(w, "Empty clock time", http.StatusBadRequest)
		return
	}
	var (
		waiting *user
		waitOpp chan match
	)
	switch vars["clock"] {
	case "1":
		waiting = &rout.waiting1min
		waitOpp = rout.opp1min
	case "3":
		waiting = &rout.waiting3min
		waitOpp = rout.opp3min
	case "5":
		waiting = &rout.waiting5min
		waitOpp = rout.opp5min
	case "10":
		waiting = &rout.waiting10min
		waitOpp = rout.opp10min
	default:
		http.Error(w, "Invalid clock time: " + vars["clock"], http.StatusBadRequest)
		return
	}

	playRoomId, color, opp := rout.newMatch(uid, username, waiting, waitOpp)

	res := map[string]string{
		"color": color,
		"roomId": playRoomId,
		"opp": opp,
	}

	resB, err := json.Marshal(res)
	if err != nil {
		log.Println("Could not marshal response:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	if _, err := w.Write(resB); err != nil {
		log.Println(err)
	}
}

func (rout *router) handleGame(w http.ResponseWriter, r *http.Request) {
	session, _ := rout.store.Get(r, "sess")
	uidBlob := session.Values["uid"]
	var uid string
	var ok bool
	if uid, ok = uidBlob.(string); !ok {
		log.Println("Unknown user")
		http.Error(w, "Unknown user", http.StatusUnauthorized)
		return
	}
	vars := mux.Vars(r)
	gameId := vars["id"]
	match, ok := rout.matches[gameId]
	if !ok {
		log.Printf("Match %v not found\n", gameId)
		http.Error(w, "Match not found", http.StatusNotFound)
		return
	}
	color := ""
	switch uid {
	case match.white.id:
		color = "white"
	case match.black.id:
		color = "black"
	default:
		log.Println("User is neither black nor white")
		http.Error(w, "User is neither black nor white", http.StatusBadRequest)
		return
	}
	cleanup := func() {
		rout.m.Lock()
		delete(rout.matches, gameId)
		rout.m.Unlock()
		rout.ldHub.finishGame<- match
	}
	switchColors := func() {
		rout.m.Lock()
		temp := match.white
		match.white = match.black
		match.black = temp
		rout.matches[gameId] = match
		rout.m.Unlock()
	}
	if vars["clock"] == "" {
		log.Println("Unset clock")
		http.Error(w, "Unset clock", http.StatusBadRequest)
		return
	}
	clock, err := strconv.Atoi(vars["clock"])
	if err != nil {
		log.Println("Invalid clock")
		http.Error(w, "Invalid clock", http.StatusBadRequest)
		return
	}
	usernameBlob := session.Values["username"]
	username, ok := usernameBlob.(string)
	if !ok {
		username = DEFAULT_USERNAME
	}
	rout.serveGame(w, r, gameId, color, clock, cleanup, switchColors, username, uid)
}

func (rout *router) handlePostUsername(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	if username == "" {
		return
	}
	session, _ := rout.store.Get(r, "sess")
	session.Values["username"] = username
	if err := rout.store.Save(r, w, session); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (rout *router) handleGetUsername(w http.ResponseWriter, r *http.Request) {
	session, _ := rout.store.Get(r, "sess")
	usernameBlob := session.Values["username"]
	if username, ok := usernameBlob.(string); ok {
		w.Write([]byte(username))
	}
}

// Set up a wait room and respond with the invitation id
func (rout *router) handleInvite(w http.ResponseWriter, r *http.Request) {
	session, err := rout.store.Get(r, "sess")
	if err != nil {
		log.Printf("Get cookie error: %v", err)
	}
	uidBlob := session.Values["uid"]
	var (
		uid string
		ok bool
	)
	if uid, ok = uidBlob.(string); !ok {
		uid = idGen.New().String()
		session.Values["uid"] = uid
		if err := rout.store.Save(r, w, session); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	usernameBlob := session.Values["username"]
	var username string
	if username, ok = usernameBlob.(string); !ok {
		username = DEFAULT_USERNAME
	}
	vars := mux.Vars(r)
	clock := vars["clock"]
	if clock == "" {
		http.Error(w, "Empty clock time", http.StatusBadRequest)
		return
	}

	// Set up room to wait for host and invited users
	var rooms map[string]*inviteRoom
	switch clock {
	case "1":
		rooms = rout.wr.rooms1min
	case "3":
		rooms = rout.wr.rooms3min
	case "5":
		rooms = rout.wr.rooms5min
	case "10":
		rooms = rout.wr.rooms10min
	default:
		http.Error(w, "Invalid clock time:" + clock, http.StatusBadRequest)
		return
	}
	inviteId := idGen.New().String()
	rout.m.Lock()
	rooms[inviteId] = &inviteRoom{
		clock: clock,
		host:  user{
			id:       uid,
			username: username,
		},
	}
	rout.m.Unlock()

	res := map[string]string{
		"inviteId": inviteId,
	}

	resB, err := json.Marshal(res)
	if err != nil {
		log.Println("Could not marshal response:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	if _, err := w.Write(resB); err != nil {
		log.Println(err)
	}
}

// Wait room for private game with a friend
func (rout *router) handleWait(w http.ResponseWriter, r *http.Request) {
	// Upgrade connection to websocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		http.Error(w, "Could not upgrade conn", http.StatusInternalServerError)
		return
	}
	defer conn.Close()
	session, _ := rout.store.Get(r, "sess")
	uidBlob := session.Values["uid"]
	var (
		uid string
		ok bool
	)
	if uid, ok = uidBlob.(string); !ok {
		uid = idGen.New().String()
		session.Values["uid"] = uid
		if err := rout.store.Save(r, w, session); err != nil {
			payload := websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error())
			conn.WriteMessage(websocket.CloseMessage, payload)
			return
		}
	}
	usernameBlob := session.Values["username"]
	username, ok := usernameBlob.(string)
	if !ok {
		username = DEFAULT_USERNAME
	}
	vars := mux.Vars(r)
	inviteId := vars["id"]
	clock := vars["clock"]
	if clock == "" {
		payload := websocket.FormatCloseMessage(websocket.CloseInvalidFramePayloadData, "Unset clock")
		conn.WriteMessage(websocket.CloseMessage, payload)
		return
	}
	var rooms map[string]*inviteRoom
	switch clock {
	case "1":
		rooms = rout.wr.rooms1min
	case "3":
		rooms = rout.wr.rooms3min
	case "5":
		rooms = rout.wr.rooms5min
	case "10":
		rooms = rout.wr.rooms10min
	default:
		payload := websocket.FormatCloseMessage(websocket.CloseInvalidFramePayloadData, "Invalid clock")
		conn.WriteMessage(websocket.CloseMessage, payload)
		return
	}
	room, ok := rooms[inviteId]
	if !ok {
		payload := websocket.FormatCloseMessage(websocket.CloseInvalidFramePayloadData, "Room not found")
		conn.WriteMessage(websocket.CloseMessage, payload)
		return
	}
	// Prepare the private channel
	rout.m.Lock()
	room.opp = make(chan match)
	rooms[inviteId] = room
	rout.m.Unlock()
	
	conn.SetReadLimit(maxMessageSize)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error { conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	cancel := make(chan bool)
	// reading goroutine
	go func() {
		defer func() {
			cancel<- true
		}()
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("error: %v", err)
				}
				break
			}
		}
	}()
	// Wait opponent for up to 1 minute
	deadline := time.NewTimer(60 * time.Second)
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		// delete waitRoom
		rout.m.Lock()
		delete(rooms, inviteId)
		rout.m.Unlock()
		ticker.Stop()
	}()
	select {
	case match := <-room.opp:
		deadline.Stop()
		if match.gameId == "" {
			payload := websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "You can't play against yourself")
			conn.WriteMessage(websocket.CloseMessage, payload)
			return
		}
		var color, opp string
		if match.white.id != "" {
			color = "black"
			match.black = user{
				id:       uid,
				username: username,
			}
			opp = match.white.username
		} else {
			color = "white"
			match.white = user{
				id: uid,
				username: username,
			}
			opp = match.black.username
		}
		rout.makeRoom(match)

		playRoomId := match.gameId
		res := map[string]string{
			"color":  color,
			"roomId": playRoomId,
			"opp":    opp,
		}
		resB, err := json.Marshal(res)
		if err != nil {
			log.Println("Could not marshal response:", err)
			payload := websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error())
			conn.WriteMessage(websocket.CloseMessage, payload)
			return
		}

		payload := websocket.FormatCloseMessage(websocket.CloseNormalClosure, string(resB))
		conn.WriteMessage(websocket.CloseMessage, payload)
	case <-deadline.C:
		payload := websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "Time is out - Link expired")
		conn.WriteMessage(websocket.CloseMessage, payload)
	case <-cancel:
	}
}

// Join game from invite link
func (rout *router) handleJoin(w http.ResponseWriter, r *http.Request) {
	session, _ := rout.store.Get(r, "sess")
	uidBlob := session.Values["uid"]
	var (
		uid string
		ok  bool
	)
	if uid, ok = uidBlob.(string); !ok {
		uid = idGen.New().String()
		session.Values["uid"] = uid
		if err := rout.store.Save(r, w, session); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	usernameBlob := session.Values["username"]
	var username string
	if username, ok = usernameBlob.(string); !ok {
		username = DEFAULT_USERNAME
	}
	vars := mux.Vars(r)
	inviteId := vars["id"]
	if inviteId == "" {
		http.Error(w, "Empty invite link", http.StatusBadRequest)
		return
	}
	clock := vars["clock"]
	if clock == "" {
		http.Error(w, "Empty clock time", http.StatusBadRequest)
		return
	}
	var rooms map[string]*inviteRoom
	switch clock {
	case "1":
		rooms = rout.wr.rooms1min
	case "3":
		rooms = rout.wr.rooms3min
	case "5":
		rooms = rout.wr.rooms5min
	case "10":
		rooms = rout.wr.rooms10min
	default:
		http.Error(w, "Invalid clock: " + clock, http.StatusBadRequest)
		return
	}

	room, ok := rooms[inviteId]
	if !ok {
		http.Error(w, "Invite link not found", http.StatusNotFound)
		return
	}

	// Is it the same user?
	if room.host.id == uid {
		// Cancel invitation
		room.opp<- match{}
		return
	}

	gameId := idGen.New().String()
	match := match{
		gameId: gameId,
	}
	// Randomly choose color
	color := ""
	if rand.Intn(2) % 2 == 0 {
		color = "white"
		match.white = user{
			id: uid,
			username: username,
		}
	} else {
		color = "black"
		match.black = user{
			id: uid,
			username: username,
		}
	}
	room.opp<- match

	res := map[string]string{
		"color":  color,
		"roomId": gameId,
		"opp":    room.host.username,
	}

	resB, err := json.Marshal(res)
	if err != nil {
		log.Println("Could not marshal response:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	if _, err := w.Write(resB); err != nil {
		log.Println(err)
	}
}

func main() {
	// flag.Parse()
	key := os.Getenv("PRINCE_SESSION_KEY")
	if key == "" {
		env, err := godotenv.Read("cookie_hash.env")
		if err != nil {
			log.Fatal(err)
		}
		key = env["SESSION_KEY"]
	}
	rout := &router{
		m:        &sync.Mutex{},
		count:    0,
		matches:  make(map[string]match),
		store:    sessions.NewCookieStore([]byte(key)),
		opp1min:  make(chan match),
		opp3min:  make(chan match),
		opp5min:  make(chan match),
		opp10min: make(chan match),
		rm:       newRoomMatcher(),
		wr:       newWaitRooms(),
		ldHub:    newLivedataHub(),
	}
	go rout.rm.listenAll()
	go rout.ldHub.run()

	r := mux.NewRouter()
	r.HandleFunc("/play", rout.handlePlay).Methods("GET").Queries("clock", "{clock}")
	r.HandleFunc("/invite", rout.handleInvite).Methods("GET").Queries("clock", "{clock}")
	r.HandleFunc("/game", rout.handleGame).Queries("id", "{id}", "clock", "{clock}")
	r.HandleFunc("/wait", rout.handleWait).Queries("id", "{id}", "clock", "{clock}")
	r.HandleFunc("/join", rout.handleJoin).Queries("id", "{id}", "clock", "{clock}")
	r.HandleFunc("/username", rout.handlePostUsername).Methods("POST")
	r.HandleFunc("/username", rout.handleGetUsername).Methods("GET")
	r.HandleFunc("/livedata", rout.handleLivedata).Methods("GET")
    c := cors.New(cors.Options{
		AllowedOrigins: []string{"http://localhost:8080", "https://princechess.netlify.app"},
		AllowCredentials: true,
		// Enable Debugging for testing, consider disabling in production
		Debug: false,
	})
	handler := c.Handler(r)
	port := os.Getenv("PORT")
	addr := ":" + port
	if port == "" {
		port = "8000"
		addr = "127.0.0.1:" + port
	}
    srv := &http.Server{
        Handler: handler,
        Addr:    addr,
        // Good practice: enforce timeouts for servers you create!
        WriteTimeout: 15 * time.Second,
        ReadTimeout:  15 * time.Second,
    }

    log.Println("Listening")
    log.Fatal(srv.ListenAndServe())
}

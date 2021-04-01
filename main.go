// Copyright 2013 The Gorilla WebSocket Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	"github.com/joho/godotenv"
    "github.com/rs/cors"
	"github.com/segmentio/ksuid"
)

var port = flag.String("port", "8000", "http service address")

type router struct {
	hub          *Hub
	m            *sync.Mutex
	store        *sessions.CookieStore
	count        int
	rooms        map[string]room // map game ids to rooms
	waiting1min  user // ids of users
	waiting3min  user
	waiting5min  user
	waiting10min user
	opp1min      chan room
	opp3min      chan room
	opp5min      chan room
	opp10min     chan room
}

type room struct {
	gameId string
	white  user
	black  user
}

type user struct {
	id       string
	username string
}

func (rout *router) makeRoom(r room) {
	rout.m.Lock()
	defer rout.m.Unlock()
	rout.count++
	rout.rooms[r.gameId] = r
}

func (rout *router) waitingRoom(uid, username string, waiting *user, opp chan room) (playRoomId, color, oppUsername string) {
	deadline := time.NewTimer(5 * time.Second)
	rout.m.Lock()
	if waiting.id == "" {
		*waiting = user{
			id:       uid,
			username: username,
		}
		rout.m.Unlock()
		select {
		case room := <-opp:
			deadline.Stop()
			if room.gameId == "" {
				// game cancelled
				return
			}
			room.white = user{
				id: uid,
				username: username,
			}

			rout.makeRoom(room)
			playRoomId = room.gameId
			color = "white"
			oppUsername = room.black.username
		case <-deadline.C:
			rout.m.Lock()
			defer rout.m.Unlock()
			*waiting = user{}
			return
		}
	} else {
		if waiting.id == uid {
			// reset
			opp<- room{}
			*waiting = user{}
			rout.m.Unlock()
			return rout.waitingRoom(uid, username, waiting, opp)
		}
		playRoomId = ksuid.New().String()
		opp<- room{
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
		uid = ksuid.New().String()
		session.Values["uid"] = uid
		if err := rout.store.Save(r, w, session); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	usernameBlob := session.Values["username"]
	var username string
	if username, ok = usernameBlob.(string); !ok {
		username = "mistery"
	}
	vars := mux.Vars(r)
	if vars["clock"] == "" {
		http.Error(w, "Empty clock time", http.StatusBadRequest)
		return
	}
	var (
		waiting *user
		waitOpp chan room
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

	playRoomId, color, opp := rout.waitingRoom(uid, username, waiting, waitOpp)

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
		http.Error(w, "Unknown user", http.StatusUnauthorized)
		return
	}
	vars := mux.Vars(r)
	gameId := vars["id"]
	room, ok := rout.rooms[gameId]
	if !ok {
		http.Error(w, "Room not found", http.StatusNotFound)
		return
	}
	color := ""
	switch uid {
	case room.white.id:
		color = "white"
	case room.black.id:
		color = "black"
	default:
		http.Error(w, "User is neither black nor white", http.StatusBadRequest)
		return
	}
	cleanup := func() {
		rout.m.Lock()
		defer rout.m.Unlock()
		rout.count--
		delete(rout.rooms, gameId)
	}
	if vars["clock"] == "" {
		http.Error(w, "Unset clock", http.StatusBadRequest)
		return
	}
	clock, err := strconv.Atoi(vars["clock"])
	if err != nil {
		http.Error(w, "Invalid clock", http.StatusBadRequest)
		return
	}
	rout.serveGame(w, r, gameId, color, clock, cleanup)
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

func main() {
	flag.Parse()
	hub := newHub()
	go hub.run()
	env, err := godotenv.Read("cookie_hash.env")
	if err != nil {
		log.Fatal(err)
	}
	key := env["SESSION_KEY"]
	rout := &router{
		hub:       hub,
		m:         &sync.Mutex{},
		count:     0,
		rooms:     make(map[string]room),
		store:     sessions.NewCookieStore([]byte(key)),
		opp1min:   make(chan room),
		opp3min:   make(chan room),
		opp5min:   make(chan room),
		opp10min:  make(chan room),
	}

	r := mux.NewRouter()
	r.HandleFunc("/play", rout.handlePlay).Methods("GET").Queries("clock", "{clock}")
	r.HandleFunc("/game", rout.handleGame).Queries("id", "{id}", "clock", "{clock}")
	r.HandleFunc("/username", rout.handlePostUsername).Methods("POST")
	r.HandleFunc("/username", rout.handleGetUsername).Methods("GET")
    c := cors.New(cors.Options{
		AllowedOrigins: []string{"http://localhost:8080"},
		AllowCredentials: true,
		// Enable Debugging for testing, consider disabling in production
		Debug: false,
	})
	handler := c.Handler(r)
    srv := &http.Server{
        Handler:      handler,
        Addr:         "127.0.0.1:" + *port,
        // Good practice: enforce timeouts for servers you create!
        WriteTimeout: 15 * time.Second,
        ReadTimeout:  15 * time.Second,
    }

    log.Println("Listening")
    log.Fatal(srv.ListenAndServe())
}

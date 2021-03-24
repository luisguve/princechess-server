// Copyright 2013 The Gorilla WebSocket Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	"github.com/gorilla/securecookie"
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
	waiting3min  bool
	waiting5min  bool
	waiting10min bool
	opp3min      chan room
	opp5min      chan room
	opp10min     chan room
}

type room struct {
	gameId string
	white  string
	black  string
}

type user struct {
	uid string
}

func (rout *router) makeRoom(r room) {
	rout.m.Lock()
	defer rout.m.Unlock()
	rout.count++
	rout.rooms[r.gameId] = r
}

func (rout *router) handlePlay(w http.ResponseWriter, r *http.Request) {
	session, _ := rout.store.Get(r, "sess")
	uidBlob := session.Values["uid"]
	color := ""
	var uid string
	var ok bool
	if uid, ok = uidBlob.(string); !ok {
		uid = ksuid.New().String()
		session.Values["uid"] = uid
		if err := session.Save(r, w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	playRoomId := ""
	vars := mux.Vars(r)
	if vars["clock"] == "" {
		http.Error(w, "Empty clock time", http.StatusBadRequest)
		return
	}
	switch vars["clock"] {
	case "3":
		rout.m.Lock()
		if !rout.waiting3min {
			rout.waiting3min = true
			rout.m.Unlock()
			room := <-rout.opp3min
			room.white = uid
			rout.makeRoom(room)
			playRoomId = room.gameId
			color = "white"
		} else {
			playRoomId = ksuid.New().String()
			rout.opp3min<- room{
				gameId: playRoomId,
				black:  uid,
			}
			rout.waiting3min = false
			rout.m.Unlock()
			color = "black"
		}
	case "5":
	case "10":
	default:
		http.Error(w, "Invalid clock time: " + vars["clock"], http.StatusBadRequest)
		return
	}

	res := map[string]string{
		"color": color,
		"roomId": playRoomId,
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
	room := rout.rooms[gameId]
	color := ""
	switch uid {
	case room.white:
		color = "white"
	case room.black:
		color = "black"
	default:
		http.Error(w, "User is neither black nor white", http.StatusBadRequest)
		return
	}
	cleanup := func() {
		rout.m.Lock()
		defer rout.m.Unlock()
		delete(rout.rooms, gameId)
	}
	rout.serveGame(w, r, gameId, color, cleanup)
}

func main() {
	flag.Parse()
	hub := newHub()
	go hub.run()
	rout := &router{
		hub:       hub,
		m:         &sync.Mutex{},
		count:     0,
		rooms:     make(map[string]room),
		store:     sessions.NewCookieStore(securecookie.GenerateRandomKey(32)),
		opp3min:   make(chan room),
		opp5min:   make(chan room),
		opp10min:  make(chan room),
	}

	r := mux.NewRouter()
	r.HandleFunc("/play", rout.handlePlay).Methods("GET").Queries("clock", "{clock}")
	r.HandleFunc("/game", rout.handleGame).Queries("id", "{id}")
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

// Copyright 2013 The Gorilla WebSocket Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 512
)

var (
	newline = []byte{'\n'}
	space   = []byte{' '}
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(_ *http.Request) bool {return true},
}

// player is a middleman between the websocket connection and the hub.
type player struct {
	hub *Hub

	// The websocket connection.
	conn *websocket.Conn

	// Buffered channel of outbound messages.
	send chan []byte

	// Channel to know when the opponent's clock ran out of time.
	oppRanOut chan bool

	cleanup func()
	color string
	gameId string
	timeLeft time.Duration
	clock *time.Timer
	lastMove time.Time
}

// readPump pumps messages from the websocket connection to the hub.
//
// The application runs readPump in a per-connection goroutine. The application
// ensures that there is at most one reader on a connection by executing all
// reads from this goroutine.
func (p *player) readPump() {
	defer func() {
		p.hub.unregister <- *p
		p.conn.Close()
		p.cleanup()
	}()
	p.conn.SetReadLimit(maxMessageSize)
	p.conn.SetReadDeadline(time.Now().Add(pongWait))
	p.conn.SetPongHandler(func(string) error { p.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	for {
		_, message, err := p.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}
		// Unmarshal just to get the color
		m := move{}
		if err = json.Unmarshal(message, &m); err != nil {
			log.Println("Could not unmarshal move:", err)
			break
		}

		m.game = p.gameId
		m.move = message

		p.hub.broadcast <- m
	}
}

// writePump pumps messages from the hub to the websocket connection.
//
// A goroutine running writePump is started for each connection. The
// application ensures that there is at most one writer to a connection by
// executing all writes from this goroutine.
func (p *player) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		p.conn.Close()
	}()
	for {
		select {
		case move, ok := <-p.send:
			p.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel.
				payload := websocket.FormatCloseMessage(1001, "")
				p.conn.WriteMessage(websocket.CloseMessage, payload)
				return
			}

			w, err := p.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(move)

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			p.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := p.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-p.clock.C:
			// Player ran out ouf time
			p.hub.broadcastNoTime<- *p
			p.conn.SetWriteDeadline(time.Now().Add(writeWait))

			payload := websocket.FormatCloseMessage(1000, "OUT_OF_TIME")
			p.conn.WriteMessage(websocket.CloseMessage, payload)
			return
		case <-p.oppRanOut:
			// Opponent ran out ouf time
			p.conn.SetWriteDeadline(time.Now().Add(writeWait))

			payload := websocket.FormatCloseMessage(1000, "OPP_OUT_OF_TIME")
			p.conn.WriteMessage(websocket.CloseMessage, payload)
			return
		}
	}
}

// serveGame handles websocket requests from the peer.
func (rout *router) serveGame(w http.ResponseWriter, r *http.Request,
	gameId, color string, minutes int, cleanup func()) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	p := player{
		hub: rout.hub,
		conn: conn,
		cleanup: cleanup,
		gameId: gameId,
		color: color,
		timeLeft: time.Duration(minutes) * time.Minute,
		send: make(chan []byte, 2),
		oppRanOut: make(chan bool),
	}
	p.hub.register <- p

	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	go p.writePump()
	go p.readPump()
}

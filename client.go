// Copyright 2013 The Gorilla WebSocket Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
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
	newline = "\n"
	space   = " "
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(_ *http.Request) bool {return true},
}

// player is a middleman between the websocket connection and the hub.
type player struct {
	room *Room

	// The websocket connection.
	conn *websocket.Conn

	// Buffered channel of outbound moves.
	sendMove chan []byte

	// Buffered channel of outbound messages.
	sendChat chan message

	// Channel to know when the opponent's clock reached zero.
	oppRanOut chan bool

	// Action channels
	drawOffer       chan bool
	oppAcceptedDraw chan bool
	oppResigned     chan bool

	cleanup  func()
	color    string
	gameId   string
	timeLeft time.Duration
	clock    *time.Timer
	lastMove time.Time
	username string
	userId   string
}

type move struct {
	Color string `json:"color"`
	move  []byte
}

// Chat message
type message struct {
	Move       move   `json:"move,omitempty"`
	Text       string `json:"chat"`
	Username   string `json:"from"`
	Resign     bool   `json:"resign"`
	DrawOffer  bool   `json:"drawOffer"`
	AcceptDraw bool   `json:"acceptDraw"`
	GameOver   bool   `json:"gameOver"`
	userId     string
}

// readPump pumps messages from the websocket connection to the room's hub.
//
// The application runs readPump in a per-connection goroutine. The application
// ensures that there is at most one reader on a connection by executing all
// reads from this goroutine.
func (p *player) readPump() {
	defer func() {
		p.room.unregister <-p
		p.conn.Close()
		p.cleanup()
	}()
	p.conn.SetReadLimit(maxMessageSize)
	p.conn.SetReadDeadline(time.Now().Add(pongWait))
	p.conn.SetPongHandler(func(string) error { p.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	for {
		_, msg, err := p.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}
		// Unmarshal message just to get the color.
		m := message{}
		if err = json.Unmarshal(msg, &m); err != nil {
			log.Println("Could not unmarshal msg:", err)
			break
		}
		switch {
		case m.Move.Color != "":
			// It's a move
			m.Move.move = msg
			p.room.broadcastMove<- m.Move
		case m.Text != "":
			// It's a chat message
			text := strings.TrimSpace(strings.Replace(m.Text, newline, space, -1))
			p.room.broadcastChat<- message{
				Text:     text,
				Username: p.username,
				userId:   p.userId,
			}
		case m.Resign:
			p.room.broadcastResign<- p.color
		case m.DrawOffer:
			p.room.broadcastDrawOffer<- p.color
		case m.AcceptDraw:
			p.room.broadcastAcceptDraw<- p.color
		case m.GameOver:
			p.room.stopClocks<- true
		default:
			log.Println("Unexpected message", m)
		}
	}
}

// writePump pumps messages from the room's hub to the websocket connection.
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
		case move, ok := <-p.sendMove: // Opponent moved a piece
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
		case msg, ok := <-p.sendChat: // Chat msg
			p.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel.
				p.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if (msg.userId == p.userId) && (msg.Username == DEFAULT_USERNAME) {
				msg.Username = "you"
			}

			msgB, err := json.Marshal(msg)
			if err != nil {
				log.Println("Could not marshal data:", err)
				break
			}

			w, err := p.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				log.Println(err)
				return
			}
			w.Write(msgB)

			// Add queued chat messages to the current websocket message.
			n := len(p.sendChat)
			for i := 0; i < n; i++ {
				msg = <-p.sendChat
				if (msg.userId == p.userId) && (msg.Username == DEFAULT_USERNAME) {
					msg.Username = "you"
				}
				msgB, err := json.Marshal(msg)
				if err != nil {
					log.Println("Could not marshal data:", err)
					break
				}
				w.Write([]byte(newline))
				w.Write(msgB)
			}

			if err := w.Close(); err != nil {
				log.Println(err)
				return
			}
		case <-ticker.C: // ping
			p.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := p.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Println(err)
				return
			}
		case <-p.clock.C: // Player ran out ouf time
			// Inform the opponent about this
			p.room.broadcastNoTime<- p.color

			data := map[string]string{
				"OOT": "MY_CLOCK",
			}
			if err := sendTextMsg(data, p.conn); err != nil {
				log.Println(err)
				return
			}
		case <-p.oppRanOut: // Opponent ran out ouf time
			data := map[string]string{
				"OOT": "OPP_CLOCK",
			}
			if err := sendTextMsg(data, p.conn); err != nil {
				log.Println(err)
				return
			}
		case <-p.drawOffer: // Opponent offered draw
			data := map[string]string{
				"drawOffer": "true",
			}
			if err := sendTextMsg(data, p.conn); err != nil {
				log.Println(err)
				return
			}
		case <-p.oppAcceptedDraw: // opponent accepted draw
			data := map[string]string{
				"oppAcceptedDraw": "true",
			}
			if err := sendTextMsg(data, p.conn); err != nil {
				log.Println(err)
				return
			}
		case <-p.oppResigned: // opponent resigned
			data := map[string]string{
				"oppResigned": "true",
			}
			if err := sendTextMsg(data, p.conn); err != nil {
				log.Println(err)
				return
			}
		}
	}
}

// JSON-marshal and send message to the connection.
func sendTextMsg(data map[string]string, conn *websocket.Conn) error {
	dataB, err := json.Marshal(data)
	if err != nil {
		return err
	}

	conn.SetWriteDeadline(time.Now().Add(writeWait))

	w, err := conn.NextWriter(websocket.TextMessage)
	if err != nil {
		return err
	}
	w.Write(dataB)

	return w.Close()
}

// serveGame handles websocket requests from the peer.
func (rout *router) serveGame(w http.ResponseWriter, r *http.Request,
	gameId, color string, minutes int, cleanup func(), username, userId string) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		http.Error(w, "Could not upgrade conn", http.StatusInternalServerError)
		return
	}
	playerClock := time.NewTimer(time.Duration(minutes) * time.Minute)
	playerClock.Stop()
	p := &player{
		cleanup:         cleanup,
		clock:           playerClock,
		color:           color,
		conn:            conn,
		gameId:          gameId,
		oppRanOut:       make(chan bool),
		drawOffer:       make(chan bool),
		oppAcceptedDraw: make(chan bool),
		oppResigned:     make(chan bool),
		sendMove:        make(chan []byte, 2), // one for the clock, one for the move
		sendChat:        make(chan message, 128),
		timeLeft:        time.Duration(minutes) * time.Minute,
		userId:          userId,
		username:        username,
	}
	switch minutes {
	case 1:
		rout.wr.registerPlayer1Min<- p
	case 3:
		rout.wr.registerPlayer3Min<- p
	case 5:
		rout.wr.registerPlayer5Min<- p
	case 10:
		rout.wr.registerPlayer10Min<- p
	default:
		log.Println("Invalid clock time:", minutes)
		http.Error(w, "Invalid clock time", http.StatusBadRequest)
		return
	}

	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	go p.writePump()
	go p.readPump()
}

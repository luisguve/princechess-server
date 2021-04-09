package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/segmentio/ksuid"
)

// Send information of users connected and ongoing games
func (rout *router) handleLivedata(w http.ResponseWriter, r *http.Request) {
	// Upgrade to websocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		http.Error(w, "Could not upgrade conn", http.StatusInternalServerError)
		return
	}
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
			log.Println(err)
			payload := websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error())
			conn.WriteMessage(websocket.CloseMessage, payload)
			return
		}
	}
	client := &livedataClient{
		uid:  uid,
		hub:  rout.ldHub,
		conn: conn,
		send: make(chan livedata, 256),
	}
	rout.ldHub.register<- client

	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	go client.writePump()
	go client.readPump()
}

type livedataHub struct {
	// Players online.
	online map[string]*livedataClient

	// Number of players in ongoing games
	playing int

	// Increment number of players in ongoing games
	startGame  chan bool

	// Decrement number of players in ongoing games
	finishGame chan bool

	// Register requests from the clients.
	register   chan *livedataClient

	// Unregister requests from the clients.
	unregister chan string
}

func newLivedataHub() *livedataHub {
	return &livedataHub{
		online:     make(map[string]*livedataClient),
		playing:    0,
		startGame:  make(chan bool),
		finishGame: make(chan bool),
		register:   make(chan *livedataClient),
		unregister: make(chan string),
	}
}

func (hub *livedataHub) run() {
	for {
		select {
		case client := <-hub.register:
			hub.online[client.uid] = client
		case uid := <-hub.unregister:
			if client, ok := hub.online[uid]; ok {
				close(client.send)
				delete(hub.online, uid)
			}
		case <-hub.startGame:
			hub.playing++
		case <-hub.finishGame:
			hub.playing -= 2
		}
		// Send real-time info to every client.
		// Note: potentially a time-costly operation).
		info := livedata{
			Players: len(hub.online) + (hub.playing),
			Games:   hub.playing / 2,
		}
		for uid, client := range hub.online {
			select {
			case client.send<- info:
			default:
				close(client.send)
				delete(hub.online, uid)
			}
		}
	}
}

type livedata struct {
	Players int `json:"players"`
	Games   int `json:"games"`
}

type livedataClient struct {
	uid string
	hub *livedataHub

	conn *websocket.Conn

	// Buffered channel of outbound messages.
	send chan livedata
}

// Reading goroutine - it only reads ping messages.
func (c *livedataClient) readPump() {
	defer func() {
		c.hub.unregister<- c.uid
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}
	}
}

// Writing goroutine - it sends real-time info and ping messages to the client.
func (c *livedataClient) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case info, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				log.Println(err)
				return
			}
			infoB, err := json.Marshal(info)
			if err != nil {
				log.Println("Could not marshal info:", err)
				payload := websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error())
				c.conn.WriteMessage(websocket.CloseMessage, payload)
				return
			}
			w.Write(infoB)

			// Add queued chat messages to the current websocket message.
			n := len(c.send)
			for i := 0; i < n; i++ {
				info = <-c.send
				infoB, err = json.Marshal(info)
				if err != nil {
					log.Println("Could not marshal info:", err)
					payload := websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error())
					c.conn.WriteMessage(websocket.CloseMessage, payload)
					return
				}
				w.Write(infoB)
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// Copyright 2013 The Gorilla WebSocket Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"log"
	"time"
)

// Hub maintains the set of active clients and broadcasts messages to the
// clients.
type Hub struct {
	// Registered clients.
	// clients map[*Client]bool
	clients map[string]players

	// Inbound messages from the clients.
	// broadcast chan []byte
	broadcast chan move

	// Channel to know that a player ran out of time
	broadcastNoTime chan player

	// Register requests from the clients.
	// register chan *Client
	register chan player

	// Unregister requests from clients.
	unregister chan player

	// Games mapped to their result
	// games map[string]game
}

/*type game struct {
	id string
	players
}*/

type players struct {
	white player
	black player
}

type move struct {
	game   string
	Color  string `json="color"`
	move   []byte
	result string
}

func newHub() *Hub {
	return &Hub{
		broadcast:  make(chan move),
		register:   make(chan player),
		unregister: make(chan player),
		broadcastNoTime: make(chan player),
		clients:    make(map[string]players), // map game ids to players
	}
}

func (h *Hub) run() {
	for {
		select {
		case player := <-h.register:
			if _, ok := h.clients[player.gameId]; !ok {
				p := players{}
				switch player.color {
				case "white":
					p.white = player
				case "black":
					p.black = player
				default:
					log.Println("Invalid color player:", player.color)
				}
				h.clients[player.gameId] = p
			} else {
				switch player.color {
				case "white":
					wPlayer := h.clients[player.gameId]
					wPlayer.white = player
					h.clients[player.gameId] = wPlayer
				case "black":
					bPlayer := h.clients[player.gameId]
					bPlayer.black = player
					h.clients[player.gameId] = bPlayer
				default:
					log.Println("Invalid color player:", player.color)
				}
			}
		case p := <-h.unregister:
			if players, ok := h.clients[p.gameId]; ok {
				if players.white.send != nil {
					close(players.white.send)
				}
				if players.white.clock != nil {
					players.white.clock.Stop()
				}
				if players.black.send != nil {
					close(players.black.send)
				}
				if players.black.clock != nil {
					players.black.clock.Stop()
				}
				delete(h.clients, p.gameId)
			}
		case move := <-h.broadcast:
			players := h.clients[move.game]
			var turn, opp player
			var white, black *player

			switch move.Color {
			case "w":
				turn = players.white
				white = &turn
				opp = players.black
				black = &opp
			case "b":
				turn = players.black
				black = &turn
				opp = players.white
				white = &opp
			default:
				log.Println("Invalid color move:", move.Color)
			}

			elapsed := 0 * time.Second
			now := time.Now()

			// Update elapsed time if not the first move
			if !turn.lastMove.IsZero() && !opp.lastMove.IsZero() {
				elapsed = now.Sub(opp.lastMove)
			}
			// Opponent has moved? reset his clock
			if !opp.lastMove.IsZero() {
				opp.clock.Reset(opp.timeLeft)
			}

			turn.lastMove = now
			turn.timeLeft -= elapsed
			turn.clock.Stop()

			players.white = *white
			players.black = *black
			h.clients[move.game] = players

			// Send my time left along with my move to the opponent.
			// Also send him his time left.
			data := make(map[string]interface{})
			if err := json.Unmarshal(move.move, &data); err != nil {
				log.Println("Could not unmarshal move:", err)
			} else {
				data["oppClock"] = turn.timeLeft.Milliseconds()
				data["clock"] = opp.timeLeft.Milliseconds()
				if move.move, err = json.Marshal(data); err != nil {
					log.Println("Could not marshal data:", err)
				}
				data = map[string]interface{}{
					"oppClock": opp.timeLeft.Milliseconds(),
					"clock": turn.timeLeft.Milliseconds(),
				}
			}

			select {
			case opp.send<- move.move:
			default:
				if turn.send != nil {
					close(turn.send)
				}
				if opp.send != nil {
					close(opp.send)
				}
				delete(h.clients, move.game)
			}
			// Send me the opponent's time left.
			var oppTimeLeft []byte
			oppTimeLeft, err := json.Marshal(data)
			if err != nil {
				log.Println("Could not marshal oppTimeLeft:", err)
			} else {
				select {
				case turn.send<- oppTimeLeft:
				default:
					if turn.send != nil {
						close(turn.send)
					}
					if opp.send != nil {
						close(opp.send)
					}
					delete(h.clients, move.game)
				}
			}
		case player := <-h.broadcastNoTime:
			players, ok := h.clients[player.gameId]
			if !ok {
				break
			}
			switch player.color {
			case "white":
				// White ran out ouf time - inform black player
				players.black.oppRanOut<- true
			case "black":
				// Black ran out ouf time - inform white player
				players.white.oppRanOut<- true
			default:
				log.Println("Invalid color player:", player.color)
			}
		}
	}
}

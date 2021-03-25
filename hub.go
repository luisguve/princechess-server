// Copyright 2013 The Gorilla WebSocket Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import "log"

// Hub maintains the set of active clients and broadcasts messages to the
// clients.
type Hub struct {
	// Registered clients.
	// clients map[*Client]bool
	clients map[string]players

	// Inbound messages from the clients.
	// broadcast chan []byte
	broadcast chan move

	// Register requests from the clients.
	// register chan *Client
	register chan player

	// Unregister requests from clients.
	unregister chan player

	// Games mapped to their result
	results map[string]string
}

type game struct {
	id string
	players
}

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
		clients:    make(map[string]players), // map game ids to players
	}
}

func (h *Hub) result(gameId string) {
	return h.results[gameId]
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
				if players.black.send != nil {
					close(players.black.send)
				}
				delete(h.clients, p.gameId)
			}
		case move := <-h.broadcast:
			players := h.clients[move.game]

			switch move.Color {
			case "w":
				select {
				case players.black.send <- move.move:
				default:
					if players.white.send != nil {
						close(players.white.send)
					}
					if players.black.send != nil {
						close(players.black.send)
					}
					delete(h.clients, move.game)
				}
			case "b":
				select {
				case players.white.send <- move.move:
				default:
					if players.white.send != nil {
						close(players.white.send)
					}
					if players.black.send != nil {
						close(players.black.send)
					}
					delete(h.clients, move.game)
				}
			default:
				log.Println("Invalid color move:", move.Color)
			}
		}
	}
}

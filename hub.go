// Copyright 2013 The Gorilla WebSocket Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"log"
)

type players struct {
	white *player
	black *player
}

// waitRoom listens for players and matches them according to the minutes specified.
type waitRoom struct {
	// Rooms mapped to players.
	rooms1Min  map[string]players
	rooms3Min  map[string]players
	rooms5Min  map[string]players
	rooms10Min map[string]players

	// Inbound channels to register players into rooms.
	registerPlayer1Min  chan *player
	registerPlayer3Min  chan *player
	registerPlayer5Min  chan *player
	registerPlayer10Min chan *player

	// Channels to notify when a game finished
	finish1MinGame  chan string
	finish3MinGame  chan string
	finish5MinGame  chan string
	finish10MinGame chan string
}

func newWaitRoom() *waitRoom {
	return &waitRoom{
		rooms1Min:           make(map[string]players),
		rooms3Min:           make(map[string]players),
		rooms5Min:           make(map[string]players),
		rooms10Min:          make(map[string]players),
		registerPlayer1Min:  make(chan *player),
		registerPlayer3Min:  make(chan *player),
		registerPlayer5Min:  make(chan *player),
		registerPlayer10Min: make(chan *player),
	}
}

func (*waitRoom) listen(register chan *player, finishGame chan string, rooms map[string]players) {
	for {
		MatchSelector:
		select {
		case p := <-register:
			pp := rooms[p.gameId]
			switch p.color {
			case "white":
				pp.white = p
			case "black":
				pp.black = p
			default:
				log.Println("Invalid color player:", p.color)
				break MatchSelector
			}
			// Set up room if both players have joined
			if (pp.white != nil) && (pp.black != nil) {
				r := &Room{
					white:           pp.white,
					black:           pp.black,
					unregister:      make(chan *player),
					broadcastMove:   make(chan move),
					broadcastChat:   make(chan message),
					broadcastNoTime: make(chan *player),
					cleanup: func() {
						finishGame<- p.gameId
					},
				}
				go r.hostGame()
				pp.white.room = r
				pp.black.room = r
			}
			rooms[p.gameId] = pp
		case gameId := <-finishGame:
			log.Println("Deleting room", gameId)
			delete(rooms, gameId)
		}
	}
}

func (wr *waitRoom) listenAll() {
	go wr.listen(wr.registerPlayer1Min, wr.finish1MinGame, wr.rooms1Min)    // 1 minute games
	go wr.listen(wr.registerPlayer3Min, wr.finish3MinGame, wr.rooms3Min)    // 3 minute games
	go wr.listen(wr.registerPlayer5Min, wr.finish5MinGame, wr.rooms5Min)    // 5 minute games
	go wr.listen(wr.registerPlayer10Min, wr.finish10MinGame, wr.rooms10Min) // 10 minute games
}

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

// roomMatcher listens for players and matches them according to the minutes specified.
type roomMatcher struct {
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

func newRoomMatcher() *roomMatcher {
	return &roomMatcher{
		rooms1Min:           make(map[string]players),
		rooms3Min:           make(map[string]players),
		rooms5Min:           make(map[string]players),
		rooms10Min:          make(map[string]players),
		registerPlayer1Min:  make(chan *player),
		registerPlayer3Min:  make(chan *player),
		registerPlayer5Min:  make(chan *player),
		registerPlayer10Min: make(chan *player),
		finish1MinGame:      make(chan string),
		finish3MinGame:      make(chan string),
		finish5MinGame:      make(chan string),
		finish10MinGame:     make(chan string),
	}
}

func (*roomMatcher) listen(register chan *player, finishGame chan string, rooms map[string]players) {
	for {
		MatchSelector:
		select {
		case p := <-register:
			pp := rooms[p.gameId]
			// See if user is reconnecting
			if pp.white != nil && pp.black != nil {
				pp.white.room.reconnect<- p
				break
			}
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
					white:                  pp.white,
					black:                  pp.black,
					duration:               p.timeLeft,
					unregister:             make(chan *player),
					broadcastMove:          make(chan move),
					broadcastChat:          make(chan message),
					broadcastNoTime:        make(chan string),
					broadcastDrawOffer:     make(chan string),
					broadcastAcceptDraw:    make(chan string),
					broadcastResign:        make(chan string),
					broadcastRematchOffer:  make(chan string),
					broadcastAcceptRematch: make(chan string),
					stopClocks:             make(chan bool),
					cleanup: func() {
						finishGame<- p.gameId
						p.cleanup()
					},
					switchColors: p.switchColors,
					disconnect:   make(chan *player),
					reconnect:    make(chan *player),
				}
				go r.hostGame()
				pp.white.room = r
				pp.black.room = r
			}
			rooms[p.gameId] = pp
		case gameId := <-finishGame:
			delete(rooms, gameId)
		}
	}
}

func (wr *roomMatcher) listenAll() {
	go wr.listen(wr.registerPlayer1Min, wr.finish1MinGame, wr.rooms1Min)    // 1 minute games
	go wr.listen(wr.registerPlayer3Min, wr.finish3MinGame, wr.rooms3Min)    // 3 minute games
	go wr.listen(wr.registerPlayer5Min, wr.finish5MinGame, wr.rooms5Min)    // 5 minute games
	go wr.listen(wr.registerPlayer10Min, wr.finish10MinGame, wr.rooms10Min) // 10 minute games
}

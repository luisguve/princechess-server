package main

import (
	"encoding/json"
	"log"
	"time"
)

// Room maintains a couple of active clients (black & white) and broadcasts
// messages to them.
type Room struct {
	white *player
	black *player

	// Unregister players.
	unregister chan *player

	// Inbound moves from the players.
	broadcastMove chan move

	// Inbound chat messages from the players.
	broadcastChat chan message

	// Channel to listen to when one of the players' clocks reached zero.
	broadcastNoTime chan *player

	// Cleanup routine after the game ends
	cleanup func()
}

func (r Room) hostGame() {
	defer r.cleanup()
	defer func() {
		if r.white.sendMove != nil {
			close(r.white.sendMove)
		}
		if r.white.clock != nil {
			r.white.clock.Stop()
		}
		if r.black.sendMove != nil {
			close(r.black.sendMove)
		}
		if r.black.clock != nil {
			r.black.clock.Stop()
		}
	}()
	for {
		ChannelSelector:
		select {
		case <-r.unregister:
			return
		case msg := <-r.broadcastChat:
			select {
			case r.white.sendChat<- msg:
			default:
				log.Println("Returning: white's chat channel buffer is full")
				return
			}
			select {
			case r.black.sendChat<- msg:
			default:
				log.Println("Returning: black's chat channel buffer is full")
				return
			}
		case move := <-r.broadcastMove:
			var turn, opp *player

			switch move.Color {
			case "w":
				turn = r.white
				opp = r.black
			case "b":
				turn = r.black
				opp = r.white
			default:
				log.Println("Invalid color move:", move.Color)
				break ChannelSelector
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

			// Send my time left along with my move to the opponent.
			// Also send him his time left.
			data := make(map[string]interface{})
			err := json.Unmarshal(move.move, &data)
			if err != nil {
				log.Println("Could not unmarshal move:", err)
				break
			}

			data["oppClock"] = turn.timeLeft.Milliseconds()
			data["clock"] = opp.timeLeft.Milliseconds()
			if move.move, err = json.Marshal(data); err != nil {
				log.Println("Could not marshal data:", err)
				break
			}
			data = map[string]interface{}{
				"oppClock": opp.timeLeft.Milliseconds(),
				"clock": turn.timeLeft.Milliseconds(),
			}

			select {
			case opp.sendMove<- move.move:
			default: return
			}
			// Send me the opponent's time left.
			var oppTimeLeft []byte
			if oppTimeLeft, err = json.Marshal(data); err != nil {
				log.Println("Could not marshal oppTimeLeft:", err)
				break
			}
			select {
			case turn.sendMove<- oppTimeLeft:
			default: return
			}
		case player := <-r.broadcastNoTime:
			// Who ran out of time?
			switch player.color {
			case "white":
				// White ran out ouf time - inform black player
				r.black.oppRanOut<- true
			case "black":
				// Black ran out ouf time - inform white player
				r.white.oppRanOut<- true
			default:
				log.Println("Invalid color player:", player.color)
				return
			}
		}
	}
}

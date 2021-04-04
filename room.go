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

	// Duration of the game in minutes
	duration time.Duration

	// Unregister players.
	unregister chan *player

	// Inbound moves from the players.
	broadcastMove chan move

	// Inbound chat messages from the players.
	broadcastChat chan message

	// Channel to listen to when one of the players' clocks reached zero.
	broadcastNoTime chan string

	// Inbound player color offering draw
	broadcastDrawOffer chan string

	// Inbound player color accepting draw
	broadcastAcceptDraw chan string

	// Inbound player color resigning
	broadcastResign chan string

	// Channel to listen to when the game is over by checkmate, prince promoted,
	// stalemate or drawn position.
	stopClocks chan bool

	// Inbound player color offering rematch
	broadcastRematchOffer chan string

	// Inbound player color accepting rematch
	broadcastAcceptRematch chan string

	// Cleanup routine after the game ends
	cleanup func()
}

func (r Room) stopTimers() {
	if r.white.clock != nil {
		r.white.clock.Stop()
	}
	if r.black.clock != nil {
		r.black.clock.Stop()
	}
}

func (r Room) hostGame() {
	defer r.cleanup()
	defer func() {
		if r.white.sendMove != nil {
			close(r.white.sendMove)
		}
		if r.black.sendMove != nil {
			close(r.black.sendMove)
		}
		r.stopTimers()
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
		case playerColor := <-r.broadcastNoTime:
			// Who ran out of time?
			switch playerColor {
			case "white":
				// White ran out ouf time - inform black player
				r.black.oppRanOut<- true
			case "black":
				// Black ran out ouf time - inform white player
				r.white.oppRanOut<- true
			default:
				log.Println("Invalid color player:", playerColor)
				return
			}
		case playerColor := <-r.broadcastDrawOffer:
			// Who is offering draw?
			switch playerColor {
			case "white":
				// Send draw offer to black player.
				r.black.drawOffer<- true
			case "black":
				// Send draw offer to white player.
				r.white.drawOffer<- true
			default:
				log.Println("Invalid color player:", playerColor)
				return
			}
		case playerColor := <-r.broadcastAcceptDraw:
			// Who is accepting draw?
			switch playerColor {
			case "white":
				// Send draw accept signal to black player.
				r.black.oppAcceptedDraw<- true
			case "black":
				// Send draw accept signal to white player.
				r.white.oppAcceptedDraw<- true
			default:
				log.Println("Invalid color player:", playerColor)
				return
			}
			r.stopTimers()
		case playerColor := <-r.broadcastResign:
			// Who is resigning?
			switch playerColor {
			case "white":
				// White resigned - inform black player
				r.black.oppResigned<- true
			case "black":
				// Black resigned - inform white player
				r.white.oppResigned<- true
			default:
				log.Println("Invalid color player:", playerColor)
				return
			}
			r.stopTimers()
		case <-r.stopClocks:
			r.stopTimers()
		case playerColor := <-r.broadcastRematchOffer:
			// Who is offering rematch?
			switch playerColor {
			case "white":
				// Send rematch offer to black player
				r.black.rematchOffer<- true
			case "black":
				// Send rematch offer to white player
				r.white.rematchOffer<- true
			default:
				log.Println("Invalid color player:", playerColor)
				return
			}
		case playerColor := <-r.broadcastAcceptRematch:
			// Who is accepting the rematch?
			switch playerColor {
			case "white":
				// Send rematch response to black player
				r.black.oppAcceptedRematch<- true
			case "black":
				// Send rematch response to white player
				r.white.oppAcceptedRematch<- true
			default:
				log.Println("Invalid color player:", playerColor)
				return
			}
			// Switch colors and reset clocks
			r.white, r.black = switchColors(r.white, r.black)
			r.white.timeLeft = r.duration
			r.white.lastMove = time.Time{}
			r.black.timeLeft = r.duration
			r.black.lastMove = time.Time{}
		}
	}
}


func switchColors(white, black *player) (*player, *player) {
	white.color = "black"
	black.color = "white"
	return black, white
}

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

	// Callback to switch colors on rematch
	switchColors func()

	// Channel to listen to when one of the players disconnects
	disconnect chan *player
	// Channel to listen to when one of the players reconnects
	reconnect chan *player
	// Variable to know when one of the players disconnected
	waitingPlayer bool
	waitingTimer *time.Timer

	pgn string
}

func (r Room) stopTimers() {
	if r.white.clock != nil {
		r.white.clock.Stop()
	}
	if r.black.clock != nil {
		r.black.clock.Stop()
	}
}

func (r *Room) hostGame() {
	defer r.cleanup()
	defer func() {
		if r.white.sendMove != nil {
			close(r.white.sendMove)
		}
		if r.black.sendMove != nil {
			close(r.black.sendMove)
		}
		if r.waitingTimer != nil {
			r.waitingTimer.Stop()
		}
		r.stopTimers()
	}()
	// Inform both players that the opponent is ready.
	r.white.oppReady<- true
	r.black.oppReady<- true
	for {
		ChannelSelector:
		select {
		case p := <-r.disconnect:
			p.disconnect<- true
			if r.waitingPlayer {
				// Both players left the room
				return
			}
			var notify *player
			switch p.color {
			case "white":
				// White disconnected - inform black player
				notify = r.black
			case "black":
				// Black disconnected - inform white player
				notify = r.white
			default:
				log.Println("Invalid color player:", p.color)
				return
			}
			notify.oppDisconnected<- true
			// Wait player for 25 seconds
			r.waitingTimer = time.AfterFunc(5 * time.Second, func() {
				notify.oppGone<- true
			})
			r.waitingPlayer = true
		case p := <-r.reconnect:
			r.waitingTimer.Stop()
			r.waitingPlayer = false
			switch p.color {
			case "white":
				// reset player clock
				p.clock = r.white.clock
				p.lastMove = r.white.lastMove
				p.timeLeft = r.white.timeLeft
				// set room
				p.room = r
				// reset player
				r.white = p
				// White reconnected - inform black player
				r.black.oppReconnected<- true
			case "black":
				// reset player clock
				p.clock = r.black.clock
				p.lastMove = r.black.lastMove
				p.timeLeft = r.black.timeLeft
				// set room
				p.room = r
				// reset player
				r.black = p
				// Black reconnected - inform white player
				r.white.oppReconnected<- true
			default:
				log.Println("Invalid color player:", p.color)
				return
			}
			data := map[string]string{
				"pgn": r.pgn,
			}
			pgn, err := json.Marshal(data)
			if err != nil {
				log.Println("Could not marshal data:", err)
				break
			}
			select {
			case p.sendMove<- pgn:
			default:
				return
			}
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
			// Save pgn
			r.pgn = move.Pgn
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
				"clock":    turn.timeLeft.Milliseconds(),
			}

			select {
			case opp.sendMove<- move.move:
			default:
				// Opponent's connection was lost.
			}
			// Send me the opponent's time left.
			var oppTimeLeft []byte
			if oppTimeLeft, err = json.Marshal(data); err != nil {
				log.Println("Could not marshal oppTimeLeft:", err)
				break
			}
			select {
			case turn.sendMove<- oppTimeLeft:
			default:
				// Turn's connection was lost.
			}
		case playerColor := <-r.broadcastNoTime:
			if r.waitingPlayer {
				break
			}
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
			if r.waitingPlayer {
				break
			}
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
			if r.waitingPlayer {
				break
			}
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
			if r.waitingPlayer {
				break
			}
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
			if r.waitingPlayer {
				break
			}
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
			if r.waitingPlayer {
				break
			}
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
			r.switchColors()
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

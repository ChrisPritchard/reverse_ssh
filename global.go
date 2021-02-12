package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"syscall"
	"unsafe"

	"golang.org/x/crypto/ssh"
)

type channelHandler func(sshConn ssh.Conn, newChannel ssh.NewChannel)

type channelOpenDirectMsg struct {
	Raddr string
	Rport uint32
	Laddr string
	Lport uint32
}

func handleChannels(sshConn ssh.Conn, chans <-chan ssh.NewChannel, handlers map[string]channelHandler) {
	// Service the incoming Channel channel in go routine
	for newChannel := range chans {
		t := newChannel.ChannelType()

		if callBack, ok := handlers[t]; ok {
			go callBack(sshConn, newChannel)
			continue
		}

		newChannel.Reject(ssh.UnknownChannelType, fmt.Sprintf("unknown channel type: %s", t))
		log.Printf("Client %s (%s) sent invalid channel type '%s'\n", sshConn.RemoteAddr(), sshConn.ClientVersion(), t)
	}
}

func sendRequest(req ssh.Request, sshChan ssh.Channel) (bool, error) {
	return sshChan.SendRequest(req.Type, req.WantReply, req.Payload)
}

// =======================

// parseDims extracts terminal dimensions (width x height) from the provided buffer.
func parseDims(b []byte) (uint32, uint32) {
	w := binary.BigEndian.Uint32(b)
	h := binary.BigEndian.Uint32(b[4:])
	return w, h
}

// ======================

// Winsize stores the Height and Width of a terminal.
type Winsize struct {
	Height uint16
	Width  uint16
	x      uint16 // unused
	y      uint16 // unused
}

// SetWinsize sets the size of the given pty.
func SetWinsize(fd uintptr, w, h uint32) {
	ws := &Winsize{Width: uint16(w), Height: uint16(h)}
	syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCSWINSZ), uintptr(unsafe.Pointer(ws)))
}

// Borrowed from https://github.com/creack/termios/blob/master/win/win.go

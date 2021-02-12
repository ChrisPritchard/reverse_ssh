package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"sync"

	"github.com/kr/pty"
	"golang.org/x/crypto/ssh"
)

func client() {

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}

	bytes, err := x509.MarshalPKCS8PrivateKey(priv) // Convert a generated ed25519 key into a PEM block so that the ssh library can ingest it, bit round about tbh
	if err != nil {
		log.Fatal("x509 marshling failed: ", err)
	}

	privatePem := pem.EncodeToMemory(
		&pem.Block{
			Type:  "PRIVATE KEY",
			Bytes: bytes,
		},
	)

	sshPriv, err := ssh.ParsePrivateKey(privatePem)
	if err != nil {
		log.Fatal("Parsing the ssh private key failed: ", err)
	}

	config := &ssh.ClientConfig{
		User: "nothing",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(sshPriv),
		},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return nil // Accept any host, also a temp dev shim
		},
	}

	addr := "localhost:2200"

	conn, err := net.DialTimeout("tcp", addr, config.Timeout)
	if err != nil {
		log.Fatal(err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		log.Fatal(err)
	}
	defer sshConn.Close()

	rq := <-reqs // To signal to the server we are a remote controllable host, we answer the question of "are you reverse" via OOB requests
	if rq.Type == "reverse?" {
		rq.Reply(true, nil)
	}

	go ssh.DiscardRequests(reqs) // Then go on to ignore everything else

	handleChannels(sshConn, chans, map[string]channelHandler{
		"session":      clientHandleNewChannel,
		"direct-tcpip": clientHandleProxyChannel,
	})

}

func clientHandleProxyChannel(sshConn ssh.Conn, newChannel ssh.NewChannel) {
	a := newChannel.ExtraData()

	var drtMsg channelOpenDirectMsg
	err := ssh.Unmarshal(a, &drtMsg)
	if err != nil {
		log.Println(err)
		return
	}

	connection, requests, err := newChannel.Accept()
	defer connection.Close()
	go func() {
		for r := range requests {
			log.Println("Got req: ", r)
		}
	}()

	tcpConn, err := net.Dial("tcp", fmt.Sprintf("%s:%d", drtMsg.Raddr, drtMsg.Rport))
	if err != nil {
		log.Println(err)
		return
	}
	defer tcpConn.Close()

	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		io.Copy(connection, tcpConn)
		wg.Done()
	}()
	go func() {
		io.Copy(tcpConn, connection)
		wg.Done()
	}()

	wg.Wait()
}

//This basically handles exactly like a SSH server would
func clientHandleNewChannel(sshConn ssh.Conn, newChannel ssh.NewChannel) {

	// At this point, we have the opportunity to reject the client's
	// request for another logical connection
	connection, requests, err := newChannel.Accept()
	if err != nil {
		log.Printf("Could not accept channel (%s)", err)
		return
	}

	// Fire up bash for this session
	bash := exec.Command("bash")

	// Prepare teardown function
	close := func() {
		connection.Close() // Not a fan of this
		_, err := bash.Process.Wait()
		if err != nil {
			log.Printf("Failed to exit bash (%s)", err)
		}
		log.Printf("Session closed")
	}

	// Allocate a terminal for this channel
	log.Print("Creating pty...")
	bashf, err := pty.Start(bash)
	if err != nil {
		log.Printf("Could not start pty (%s)", err)
		close()
		return
	}

	//pipe session to bash and visa-versa
	var once sync.Once
	go func() {
		io.Copy(connection, bashf)
		once.Do(close)
	}()
	go func() {
		io.Copy(bashf, connection)
		once.Do(close)
	}()

	for req := range requests {
		log.Println("Got request: ", req.Type)
		switch req.Type {
		case "shell":
			// We only accept the default shell
			// (i.e. no command in the Payload)
			if len(req.Payload) == 0 {
				req.Reply(true, nil)
			}
		case "pty-req":
			termLen := req.Payload[3]
			w, h := parseDims(req.Payload[termLen+4:])
			SetWinsize(bashf.Fd(), w, h)
			// Responding true (OK) here will let the client
			// know we have a pty ready for input
			req.Reply(true, nil)
		case "window-change":
			w, h := parseDims(req.Payload)
			SetWinsize(bashf.Fd(), w, h)
		}
	}

}

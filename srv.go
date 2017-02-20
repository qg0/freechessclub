// Copyright © 2017 The Free Chess Club <help@freechess.club>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ziutek/telnet"
	"github.com/pkg/errors"
)

const (
	loginPrompt = "login:"
	passwordPrompt = "password:"
	ficsPrompt = "fics%"
)

var (
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
)

// message sent to us by the javascript client
type message struct {
	Handle string `json:"handle"`
	Text   string  `json:"text"`
}

type Session struct {
	conn *telnet.Conn
	ws   *websocket.Conn
	username string
}

// validateMessage so that we know it's valid JSON and contains a Handle and
// Text
func validateMessage(data []byte) (message, error) {
	var msg message

	if err := json.Unmarshal(data, &msg); err != nil {
		return msg, errors.Wrap(err, "Unmarshaling message")
	}

	if msg.Text == "" {
		return msg, errors.New("Message has no Text")
	}
	return msg, nil
}

func Connect(network, addr string, timeout, retries int) (*telnet.Conn, error) {
	ts := time.Duration(timeout) * time.Second

	var conn *telnet.Conn
	var connected bool = false
	var err error = nil

	for attempts := 1; attempts <= retries && connected != true; attempts++ {
		log.Printf("Connecting to chess server %s (attempt %d of %d)...", addr, attempts, retries)
		conn, err = telnet.DialTimeout(network, addr, ts)
		if err != nil {
			continue
		}
		connected = true
	}
	if err != nil  || connected == false {
		return nil, fmt.Errorf("error connecting to server %s: %v", addr, err)
	}
	log.Printf("Connected!")

  conn.SetUnixWriteMode(true)
	conn.SetReadDeadline(time.Now().Add(ts))
	conn.SetWriteDeadline(time.Now().Add(ts))
	return conn, nil
}

func send(conn *telnet.Conn, cmd string) error {
	conn.SetWriteDeadline(time.Now().Add(20 * time.Second))
	buf := make([]byte, len(cmd)+1)
	copy(buf, cmd)
	buf[len(cmd)] = '\n'
	_, err := conn.Write(buf)
	return err
}

func sendAndReadUntil(conn *telnet.Conn, cmd string, delims ...string) ([]byte, error) {
	err := send(conn, cmd)
	if err != nil {
		return nil, err
	}
	return conn.ReadUntil(delims...)
}

func Login(conn *telnet.Conn, username, password string) (string, error) {
	var prompt string
	// guests have no passwords
	if (username != "guest") {
		prompt = passwordPrompt
	} else {
		prompt = "Press return to enter the server as"
		password = ""
	}

	// wait for the login prompt
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	conn.ReadUntil(loginPrompt)
	//fmt.Println(string(r[:]))
	out, err := sendAndReadUntil(conn, username, prompt)
	if err != nil {
		return "", fmt.Errorf("error creating new login session for %s: %v", username, err)
	}

	re := regexp.MustCompile("Logging you in as \"([a-zA-Z]+)\"")
	user := re.FindSubmatch(out)
	if user != nil {
		username = string(user[1][:])
	}

	// wait for the password prompt
	_, err = sendAndReadUntil(conn, password, ficsPrompt)
	if err != nil {
		return "", fmt.Errorf("failed authentication for %s (password %s): %v", username, password, err)
	}

	log.Printf("Logged in as %s", username)

	//fmt.Println(string(motd[:]))
	return username, nil
}

func (s *Session) ficsReader() {
	for {
		s.conn.SetReadDeadline(time.Now().Add(3600 * time.Second))
		out, err := s.conn.ReadUntil(ficsPrompt)
		if err != nil {
			s.ws.WriteMessage(websocket.CloseMessage, []byte{})
			log.Println("Closing session.")
			return
		}

		out = bytes.TrimSuffix(out, []byte(ficsPrompt))

		username := s.username
		msg := string(out[:])
		re := regexp.MustCompile(`([a-zA-Z]+)(?:\(.+\)+)?\(53\):(.*)(?:\(told [0-9]+ players in channel [0-9]+.*\))?`)
		matches := re.FindSubmatch(out)
		if matches != nil {
			username = string(matches[1][:])
			msg = string(matches[2][:])
		}
		sendWebsocket(s.ws, username, msg)
	}
}

func (s *Session) send(msg string) error {
	return send(s.conn, msg)
}

func newSession(ws *websocket.Conn) *Session {
	conn, err := Connect("tcp", "freechess.org:5000", 5, 5)
	if err != nil {
		log.Fatal(err)
	}

	username, err := Login(conn, "guest", "")
	if err != nil {
		log.Fatal(err)
	}

	sendWebsocket(ws, username, "")

	_, err = sendAndReadUntil(conn, "set interface fcc", ficsPrompt)
	if err != nil {
		log.Fatal(err)
	}

	_, err = sendAndReadUntil(conn, "set seek 0", ficsPrompt)
	if err != nil {
		log.Fatal(err)
	}

	s := &Session{
		conn: conn,
		ws: ws,
		username: username,
	}

	go s.ficsReader()
	return s
}

func (s *Session) end() {
	s.ws.WriteMessage(websocket.CloseMessage, []byte{})
	send(s.conn, "exit")
	s.conn.Close()
}


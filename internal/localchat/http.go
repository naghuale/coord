//go:build unix

package localchat

import (
	"bytes"
	"context"
	"coord/internal/rooms"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Request carries natural-language text or opaque capability callbacks.
type Request struct {
	WaitSeconds    int                 `json:"wait_seconds,omitempty"`
	Auth           rooms.SessionAuth   `json:"auth,omitempty"`
	Resume         rooms.ResumeRequest `json:"resume,omitempty"`
	Checkpoint     rooms.Checkpoint    `json:"checkpoint,omitempty"`
	Registration   string              `json:"registration,omitempty"`
	Connection     string              `json:"connection,omitempty"`
	Readiness      rooms.Readiness     `json:"readiness,omitempty"`
	Operation      string              `json:"operation,omitempty"`
	Instructions   string              `json:"instructions,omitempty"`
	IncludeContent bool                `json:"include_content,omitempty"`
	Room           string              `json:"room,omitempty"`
	Task           string              `json:"task,omitempty"`
	Title          string              `json:"title,omitempty"`
	Role           string              `json:"role,omitempty"`
	AgentID        string              `json:"agent_id,omitempty"`
	Actor          string              `json:"actor,omitempty"`
	After          uint64              `json:"after,omitempty"`
	Value          string              `json:"value,omitempty"`
	Text           string              `json:"text,omitempty"`
}

// Response contains only user-visible state; tokens never appear in status.
type Response struct {
	Ticket       *rooms.RegistrationTicket `json:"ticket,omitempty"`
	Registration *rooms.Registration       `json:"registration,omitempty"`
	Start        *rooms.StartResult        `json:"start,omitempty"`
	Invitation   *rooms.Invitation         `json:"invitation,omitempty"`
	Sessions     []rooms.Member            `json:"sessions,omitempty"`
	Session      *rooms.Member             `json:"session,omitempty"`
	Attachment   *rooms.Attachment         `json:"attachment,omitempty"`
	Context      *rooms.SessionContext     `json:"context,omitempty"`
	History      *rooms.HistoryStatus      `json:"history,omitempty"`
	Rooms        []rooms.Room              `json:"rooms,omitempty"`
	Room         *rooms.Room               `json:"room,omitempty"`
	Events       []rooms.Event             `json:"events,omitempty"`
	Event        *rooms.Event              `json:"event,omitempty"`
	Error        string                    `json:"error,omitempty"`
	Code         string                    `json:"code,omitempty"`
	Peers        []Peer                    `json:"peers,omitempty"`
	Peer         *Peer                     `json:"peer,omitempty"`
	Answer       *Answer                   `json:"answer,omitempty"`
	Status       *Status                   `json:"status,omitempty"`
}

// Handler serves only the private Unix socket; no TCP listener is created.
func (s *Service) Handler(stop func()) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/watch" {
			if !s.isOwner(r) {
				http.Error(w, "owner capability required", http.StatusForbidden)
				return
			}
			s.watch(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var response Response
		status := http.StatusOK
		var err error
		var input Request
		get := r.URL.Path == "/health" || r.URL.Path == "/peers" || r.URL.Path == "/status" || r.URL.Path == "/rooms" || r.URL.Path == "/sessions" || r.URL.Path == "/history-status"
		expected := http.MethodPost
		if get {
			expected = http.MethodGet
		}
		if r.Method != expected {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(Response{Error: "неверный метод"})
			return
		}
		if !get {
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
			d.DisallowUnknownFields()
			if err = d.Decode(&input); err == nil {
				var extra any
				if e := d.Decode(&extra); e != io.EOF {
					err = fmt.Errorf("лишние данные запроса")
				}
			}
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(Response{Error: err.Error()})
				return
			}
		}
		if ownerRequired(r.URL.Path, input) && !s.isOwner(r) {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(Response{Error: "owner capability required"})
			return
		}
		switch r.URL.Path {
		case "/health":
		case "/owner-start":
			var peer Peer
			peer, err = s.resolvePeer(r.Context(), input.Value)
			if err == nil {
				var started rooms.StartResult
				started, err = s.rooms.Start(input.Task, input.Title, input.AgentID, peer.Address, peer.Ref, input.Instructions, input.Operation)
				if err == nil {
					response.Start = &started
				}
			}
		case "/sessions":
			response.Sessions = s.rooms.Sessions()
		case "/session-register":
			var ticket rooms.RegistrationTicket
			ticket, err = s.rooms.Register(input.AgentID, input.Connection, input.Operation)
			if err == nil {
				response.Ticket = &ticket
			}
		case "/session-registration":
			var reg rooms.Registration
			reg, err = s.rooms.RegistrationStatus(input.Auth)
			if err == nil {
				response.Registration = &reg
			}
		case "/session-regulation":
			err = s.rooms.SetRegulation(input.Auth, input.Operation, input.Registration, input.Value, input.Instructions)
		case "/session-ready":
			err = s.rooms.Ready(input.Auth, input.Operation, input.Readiness)
		case "/session-invite":
			var peer Peer
			if input.Value != "" {
				peer, err = s.resolvePeer(r.Context(), input.Value)
			}
			if err == nil {
				var inv rooms.Invitation
				inv, err = s.rooms.InviteSession(input.Auth.Session, peer.Address, peer.Ref, input.Operation, input.Actor)
				if err == nil {
					response.Invitation = &inv
				}
			}
		case "/session-assign":
			var session rooms.Member
			if input.Registration != "" {
				session, err = s.rooms.AssignRegistered(input.Room, input.Registration, input.Role, input.Instructions, input.Actor)
			} else {
				session, err = s.rooms.Assign(input.Room, input.AgentID, input.Role, input.Instructions, input.Actor)
			}
			if err == nil {
				response.Session = &session
			}
		case "/session-resume":
			if input.Value != "" {
				var peer Peer
				peer, err = s.resolvePeer(r.Context(), input.Value)
				if err == nil {
					input.Resume.Ref = peer.Ref
					input.Resume.Address = peer.Address
				}
			}
			if err == nil {
				var a rooms.Attachment
				a, err = s.rooms.Resume(input.Resume)
				if err == nil {
					response.Attachment = &a
				}
			}
		case "/session-wait":
			var c rooms.SessionContext
			c, err = s.waitContext(r.Context(), input)
			if err == nil {
				response.Context = &c
			}
		case "/session-context":
			var c rooms.SessionContext
			c, err = s.rooms.Context(input.Auth, input.After)
			if err == nil {
				response.Context = &c
			}
		case "/session-say":
			var e rooms.Event
			e, err = s.rooms.SessionSay(input.Auth, input.Operation, input.Text)
			if err == nil {
				response.Event = &e
			}
		case "/session-ack":
			err = s.rooms.Ack(input.Auth, input.Operation, input.After)
		case "/session-checkpoint":
			err = s.rooms.SaveCheckpoint(input.Auth, input.Operation, input.Checkpoint)
		case "/history-status":
			h := s.rooms.HistoryStatus()
			response.History = &h
		case "/history-export":
			err = s.rooms.Export(r.Context(), input.IncludeContent)
			h := s.rooms.HistoryStatus()
			response.History = &h
		case "/rooms":
			response.Rooms = s.rooms.List()
		case "/room-create":
			var room rooms.Room
			room, err = s.rooms.Create(input.Task, input.Title, input.Actor)
			if err == nil {
				response.Room = &room
			}
		case "/invite":
			var peer Peer
			peer, err = s.resolvePeer(r.Context(), input.Value)
			if err == nil {
				err = s.rooms.Invite(input.Room, peer.Address, peer.Ref, input.Role, input.AgentID, input.Actor)
			}
		case "/say":
			var event rooms.Event
			event, err = s.rooms.Say(input.Room, input.Text, input.Actor)
			if err == nil {
				response.Event = &event
			}
		case "/events":
			response.Events, err = s.rooms.Events(input.Room, input.After)
		case "/room-close":
			err = s.rooms.CloseRoom(input.Room, input.Actor)
		case "/peers":
			response.Peers, err = s.Peers(r.Context())
		case "/status":
			value := s.Status()
			response.Status = &value
		case "/connect":
			var peer Peer
			peer, err = s.Connect(r.Context(), input.Value)
			if err == nil {
				response.Peer = &peer
			}
		case "/accept":
			if strings.HasPrefix(input.Value, "room.") {
				var a rooms.Attachment
				a, err = s.rooms.AcceptAttachment(input.Value)
				if err == nil {
					response.Attachment = &a
				}
			} else {
				err = s.Accept(input.Value)
			}
		case "/reply":
			err = s.Reply(input.Value, input.Text)
		case "/chat":
			var answer Answer
			answer, err = s.Chat(r.Context(), input.Text)
			if err == nil {
				response.Answer = &answer
			}
		case "/stop":
			if stop == nil {
				err = fmt.Errorf("stop недоступен")
			} else {
				defer stop()
			}
		default:
			status = http.StatusNotFound
			err = fmt.Errorf("неизвестная команда")
		}
		if err != nil {
			response.Error = err.Error()
			if status == http.StatusOK {
				status = http.StatusConflict
			}
			if errors.Is(err, ErrNotConnected) {
				response.Code = "not_connected"
			}
			if errors.Is(err, ErrNotConfirmed) {
				response.Code = "not_confirmed"
				status = http.StatusRequestTimeout
			}
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(response)
	})
}

// Client connects exclusively to a local Unix socket.
type Client struct {
	http  *http.Client
	owner string
}

// SocketPath is shared by CLI and daemon.
func SocketPath(state string) string { return filepath.Join(state, "coord.sock") }

// NewClient never falls back to TCP.
func NewClient(state string) *Client {
	path := SocketPath(state)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", path)
	}}
	return &Client{http: &http.Client{Transport: transport}}
}

// Close releases idle connections.
func (c *Client) Close() { c.http.CloseIdleConnections() }

// Call sends a bounded request and decodes structured errors.
func (c *Client) Call(ctx context.Context, command string, input Request) (Response, error) {
	var result Response
	raw, err := json.Marshal(input)
	if err != nil {
		return result, err
	}
	method := http.MethodPost
	if command == "health" || command == "peers" || command == "status" || command == "rooms" || command == "sessions" || command == "history-status" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://coord/"+command, bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	if c.owner != "" {
		req.Header.Set("X-Coord-Owner", c.owner)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return result, err
	}
	defer func() { _ = response.Body.Close() }()
	if err = json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&result); err != nil {
		return result, err
	}
	if response.StatusCode != http.StatusOK {
		if result.Error == "" {
			return result, fmt.Errorf("coord: HTTP %d", response.StatusCode)
		}
		switch result.Code {
		case "not_connected":
			return result, fmt.Errorf("%w", ErrNotConnected)
		case "not_confirmed":
			return result, fmt.Errorf("%w: %s", ErrNotConfirmed, result.Error)
		}
		return result, errors.New(result.Error)
	}
	return result, nil
}

// Offline identifies an absent daemon; permission and protocol failures are not
// grounds for silently replacing it.
func Offline(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}

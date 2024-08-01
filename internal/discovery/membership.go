package discovery

import (
	"net"

	"go.uber.org/zap"

	"github.com/hashicorp/raft"
	"github.com/hashicorp/serf/serf"
)

type Membership struct {
	Config                  // Config of the node
	handler Handler         // interface to manager join and leave events
	serf    *serf.Serf      // Serf client to manager membership and events
	events  chan serf.Event // channel to reciev Serf events
	logger  *zap.Logger     // Logger
}

func New(handler Handler, config Config) (*Membership, error) {
	c := &Membership{
		Config:  config,
		handler: handler,
		logger:  zap.L().Named("membership"),
	}
	if err := c.setupSerf(); err != nil {
		return nil, err
	}
	return c, nil
}

type Config struct {
	NodeName       string
	BindAddr       string            // Link address for the node
	Tags           map[string]string // Associed Tag to the node
	StartJoinAddrs []string          // Addresses of nodes to join at startup.
}

func (m *Membership) setupSerf() (err error) {
	addr, err := net.ResolveTCPAddr("tcp", m.BindAddr) // Convert BindAddr to a TCP address
	if err != nil {
		return err
	}
	config := serf.DefaultConfig() // Init config by default of Serf
	config.Init()
	// Configures the address and port of the Serf member list.
	config.MemberlistConfig.BindAddr = addr.IP.String()
	config.MemberlistConfig.BindPort = addr.Port
	m.events = make(chan serf.Event) // Create a channel to receive events from Serf.
	config.EventCh = m.events
	config.Tags = m.Tags
	config.NodeName = m.Config.NodeName
	m.serf, err = serf.Create(config) // Create the Serf client with config
	if err != nil {
		return err
	}
	go m.eventHandler()          // Init manager of events
	if m.StartJoinAddrs != nil { // Joins nodes in StartJoinAddrs if they are defined
		_, err = m.serf.Join(m.StartJoinAddrs, true)
		if err != nil {
			return err
		}
	}
	return nil
}

type Handler interface {
	Join(name, addr string) error // Method to handle when a node joins
	Leave(name string) error      // Method to handle when a node leave
}

// Event Loop: Listen to the events channel.
// Event Types: Handles join, leave, and member failure events.
// Join Events: Ignore events from the local member and handle the others with handleJoin.
// Exit/Failure Events: Similar to join, but handled with handleLeave.
func (m *Membership) eventHandler() {
	for e := range m.events {
		switch e.EventType() {
		case serf.EventMemberJoin:
			for _, member := range e.(serf.MemberEvent).Members {
				if m.isLocal(member) {
					continue
				}
				m.handleJoin(member)
			}
		case serf.EventMemberLeave, serf.EventMemberFailed:
			for _, member := range e.(serf.MemberEvent).Members {
				if m.isLocal(member) {
					return
				}
				m.handleLeave(member)
			}
		}
	}
}

func (m *Membership) handleJoin(member serf.Member) {
	if err := m.handler.Join(
		member.Name,
		member.Tags["rpc_addr"],
	); err != nil {
		m.logError(err, "failed to join", member)
	}
}

func (m *Membership) handleLeave(member serf.Member) {
	if err := m.handler.Leave(
		member.Name,
	); err != nil {
		m.logError(err, "failed to leave", member)
	}
}

func (m *Membership) isLocal(member serf.Member) bool {
	return m.serf.LocalMember().Name == member.Name
}

func (m *Membership) Members() []serf.Member {
	return m.serf.Members()
}

func (m *Membership) Leave() error {
	return m.serf.Leave()
}

func (m *Membership) logError(err error, msg string, member serf.Member) {
	log := m.logger.Error
	if err == raft.ErrNotLeader {
		log = m.logger.Debug
	}
	log(
		msg,
		zap.Error(err),
		zap.String("name", member.Name),
		zap.String("rpc_addr", member.Tags["rpc_addr"]),
	)
}

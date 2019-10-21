package main

import (
	"bufio"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/protocol"

	disc "github.com/libp2p/go-libp2p-discovery"
	"github.com/libp2p/go-libp2p/p2p/discovery"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/routing"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/multiformats/go-multiaddr"

	"github.com/davecgh/go-spew/spew"

	"github.com/manifoldco/promptui"
)

// https://hackmd.io/@raulk/taipei-ipfs-2019/edit
type TaipeiExample struct {
	sync.RWMutex

	ctx    context.Context
	h      host.Host
	dht    *dht.IpfsDHT
	pubsub *pubsub.PubSub

	mdnsPeers map[peer.ID]peer.AddrInfo
	messages  map[string][]*pubsub.Message
	streams   chan network.Stream
}

func (t *TaipeiExample) HandlePeerFound(pi peer.AddrInfo) {
	t.Lock()
	t.mdnsPeers[pi.ID] = pi
	t.Unlock()

	if err := t.h.Connect(t.ctx, pi); err != nil {
		fmt.Printf("failed to connect to mDNS peer: %s\n", err)
	}
}

func (t *TaipeiExample) Run() {
	commands := []struct {
		name string
		exec func() error
	}{
		{"My info", t.handleMyInfo},
		{"DHT: Bootstrap (public seeds)", func() error { return t.handleDHTBootstrap(dht.DefaultBootstrapPeers...) }},
		{"DHT: Bootstrap (no seeds)", func() error { return t.handleDHTBootstrap() }},
		{"DHT: Announce service", t.handleAnnounceService},
		{"DHT: Find service providers", t.handleFindProviders},
		{"Network: Connect to a peer", t.handleConnect},
		{"Network: List connections", t.handleListConnectedPeers},
		{"mDNS: List local peers", t.handleListmDNSPeers},
		{"Pubsub: Subscribe to topic", t.handleSubscribeToTopic},
		{"Pubsub: Publish a message", t.handlePublishToTopic},
		{"Pubsub: Print inbound messages", t.handlePrintInboundMessages},
		{"Protocol: Initiate chat with peer", t.handleInitiateChat},
		{"Protocol: Accept incoming chat", t.handleAcceptChat},
		{"Switch to bootstrap mode", t.handleBootstrapMode},
	}

	var str []string
	for _, c := range commands {
		str = append(str, c.name)
	}

	for {
		sel := promptui.Select{
			Label: "What do you want to do?",
			Items: str,
			Size:  1000,
		}

		fmt.Println()
		i, _, err := sel.Run()
		if err != nil {
			panic(err)
		}

		if err := commands[i].exec(); err != nil {
			fmt.Printf("command failed: %s\n", err)
		}
	}
}

func (t *TaipeiExample) chatProtocolHandler(s network.Stream) {
	fmt.Printf("*** Got a new chat stream from %s! ***\n", s.Conn().RemotePeer())
	t.streams <- s
}

func main() {
	// ~~ 0c. Note that contexts are an ugly way of controlling component
	// lifecycles. Talk about the service-based host refactor.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var kaddht *dht.IpfsDHT
	newDHT := func(h host.Host) (routing.PeerRouting, error) {
		var err error
		kaddht, err = dht.New(ctx, h)
		return kaddht, err
	}

	// 0a. Let's build a new libp2p host. The New constructor uses functional
	// parameters. You don't need to provide any parameters. libp2p comes with
	// sane defaults OOTB, but in order to stay slim, we don't attach a routing
	// implementation by default. Let's do that.
	host, err := libp2p.New(ctx, libp2p.Routing(newDHT))
	if err != nil {
		panic(err)
	}

	mdns, err := discovery.NewMdnsService(ctx, host, time.Second*5, "")
	if err != nil {
		panic(err)
	}

	ps, err := pubsub.NewGossipSub(ctx, host)
	if err != nil {
		panic(err)
	}

	ex := &TaipeiExample{
		ctx:       ctx,
		h:         host,
		dht:       kaddht,
		mdnsPeers: make(map[peer.ID]peer.AddrInfo),
		messages:  make(map[string][]*pubsub.Message),
		streams:   make(chan network.Stream, 128),
		pubsub:    ps,
	}

	host.SetStreamHandler(protocol.ID("/taipei/chat/2019"), ex.chatProtocolHandler)

	mdns.RegisterNotifee(ex)

	ex.Run()
}

func (t *TaipeiExample) handleConnect() error {
	p := promptui.Prompt{
		Label:    "multiaddr",
		Validate: validateMultiaddr,
	}
	addr, err := p.Run()
	if err != nil {
		return err
	}
	ma, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return err
	}
	ai, err := peer.AddrInfoFromP2pAddr(ma)
	ctx, cancel := context.WithTimeout(t.ctx, 30*time.Second)
	defer cancel()
	return t.h.Connect(ctx, *ai)
}

func (t *TaipeiExample) handleListConnectedPeers() error {
	for _, c := range t.h.Network().Conns() {
		fmt.Println("connected to", c.RemotePeer(), "on", c.RemoteMultiaddr())
	}

	return nil
}

func (t *TaipeiExample) handleBootstrapMode() error {
	fmt.Println("this node will now serve as a DHT bootstrap node, addrs:")
	fmt.Println("peer ID:", t.h.ID())
	fmt.Println("addrs:", t.h.Addrs())
	time.Sleep(24 * time.Hour)
	return nil
}

func (t *TaipeiExample) handleDHTBootstrap(seeds ...multiaddr.Multiaddr) error {
	fmt.Println("Will bootstrap for 30 seconds...")

	ctx, cancel := context.WithTimeout(t.ctx, 30*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(len(seeds))

	for _, ma := range seeds {
		ai, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			return err
		}

		go func(ai peer.AddrInfo) {
			defer wg.Done()

			fmt.Printf("Connecting to peer: %s\n", ai)
			if err := t.h.Connect(ctx, ai); err != nil {
				fmt.Printf("Failed while connecting to peer: %s; %s\n", ai, err)
			} else {
				fmt.Printf("Succeeded while connecting to peer: %s\n", ai)
			}
		}(*ai)
	}

	wg.Wait()

	if err := t.dht.BootstrapRandom(ctx); err != nil && err != context.DeadlineExceeded {
		return fmt.Errorf("failed while bootstrapping DHT: %w", err)
	}

	fmt.Println("bootstrap OK! Routing table:")
	t.dht.RoutingTable().Print()

	return nil
}

func (t *TaipeiExample) handleListmDNSPeers() error {
	t.RLock()
	defer t.RUnlock()

	spew.Dump(t.mdnsPeers)
	return nil
}

func (t *TaipeiExample) handleAnnounceService() error {
	rd := disc.NewRoutingDiscovery(t.dht)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := rd.Advertise(ctx, "taipei2019", disc.TTL(10*time.Minute))
	return err
}

func (t *TaipeiExample) handleFindProviders() error {
	rd := disc.NewRoutingDiscovery(t.dht)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	peers, err := rd.FindPeers(ctx, "taipei2019")
	if err != nil {
		return err
	}

	for p := range peers {
		fmt.Println("found peer", p)
		t.h.Peerstore().AddAddrs(p.ID, p.Addrs, 24*time.Hour)
	}
	return err
}

func (t *TaipeiExample) handleSubscribeToTopic() error {
	p := promptui.Prompt{
		Label: "topic name",
	}
	topic, err := p.Run()
	if err != nil {
		return err
	}

	sub, err := t.pubsub.Subscribe(topic)
	if err != nil {
		return err
	}

	go func() {
		for {
			m, err := sub.Next(t.ctx)
			if err != nil {
				fmt.Println(err)
				return
			}
			t.Lock()
			msgs := t.messages[sub.Topic()]
			t.messages[sub.Topic()] = append(msgs, m)
			t.Unlock()
		}
	}()
	return nil
}

func (t *TaipeiExample) handlePublishToTopic() error {
	p := promptui.Prompt{
		Label: "topic name",
	}
	topic, err := p.Run()
	if err != nil {
		return err
	}

	p = promptui.Prompt{
		Label: "data",
	}
	data, err := p.Run()
	if err != nil {
		return err
	}

	return t.pubsub.Publish(topic, []byte(data))
}

func (t *TaipeiExample) handlePrintInboundMessages() error {
	t.RLock()
	topics := make([]string, 0, len(t.messages))
	for k, _ := range t.messages {
		topics = append(topics, k)
	}
	t.RUnlock()

	s := promptui.Select{
		Label: "topic",
		Items: topics,
	}

	_, topic, err := s.Run()
	if err != nil {
		return err
	}

	t.Lock()
	defer t.Unlock()
	for _, m := range t.messages[topic] {
		fmt.Printf("<<< from: %s >>>: %s\n", m.GetFrom(), string(m.GetData()))
	}
	t.messages[topic] = nil
	return nil
}

func (t *TaipeiExample) handleMyInfo() error {
	// 0b. Let's get a sense of what those defaults are. What transports are we
	// listening on? Each transport will have a multiaddr. If you run this
	// multiple times, you will get different port numbers. Note how we listen
	// on all interfaces by default.
	fmt.Println("My addresses:")
	for _, a := range t.h.Addrs() {
		fmt.Printf("\t%s\n", a)
	}

	fmt.Println()
	fmt.Println("My peer ID:")
	fmt.Printf("\t%s\n", t.h.ID())

	fmt.Println()
	fmt.Println("My identified multiaddrs:")
	for _, a := range t.h.Addrs() {
		fmt.Printf("\t%s/p2p/%s\n", a, t.h.ID())
	}

	// What protocols are added by default?
	fmt.Println()
	fmt.Println("Protocols:")
	for _, p := range t.h.Mux().Protocols() {
		fmt.Printf("\t%s\n", p)
	}

	// What peers do we have in our peerstore? (hint: we've connected to nobody so far).
	fmt.Println()
	fmt.Println("Peers in peerstore:")
	for _, p := range t.h.Peerstore().PeersWithAddrs() {
		fmt.Printf("\t%s\n", p)
	}

	return nil
}

func (t *TaipeiExample) handleInitiateChat() error {
	p := promptui.Prompt{Label: "peer id"}
	id, err := p.Run()
	if err != nil {
		return err
	}
	pid, err := peer.IDB58Decode(id)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := t.h.NewStream(ctx, pid, "/taipei/chat/2019")
	if err != nil {
		return err
	}

	return handleChat(s)
}

func (t *TaipeiExample) handleAcceptChat() error {
	select {
	case s := <-t.streams:
		return handleChat(s)
	default:
		fmt.Println("no incoming chats")
	}
	return nil
}

func handleChat(s network.Stream) error {
	rw := bufio.NewReadWriter(bufio.NewReader(s), bufio.NewWriter(s))
	go func() {
		for {
			str, err := rw.ReadString('\n')
			fmt.Printf("<<<< %s\n", str)
			if err != nil {
				fmt.Println("chat closed")
				s.Close()
				return
			}
		}
	}()

	p := promptui.Prompt{Label: "message"}
	for {
		msg, err := p.Run()
		if err != nil {
			return err
		}
		if msg == "." {
			s.Close()
		}
		if _, err := rw.WriteString(msg + "\n"); err != nil {
			return err
		}
		rw.Flush()
	}
}

func validateMultiaddr(s string) error {
	ma, err := multiaddr.NewMultiaddr(s)
	if err != nil {
		return err
	}
	_, err = peer.AddrInfoFromP2pAddr(ma)
	return err
}

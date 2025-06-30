package ping

import (
	"context"
	"log"
	"net"
	"ping-monitor/models"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

const (
	maxPingAttempts = 5
	logPrefix       = "PING: "
)

func PingServer(server *models.Server, wg *sync.WaitGroup, mu *sync.Mutex, timeout time.Duration) {
	defer wg.Done()

	ipAddr, err := net.ResolveIPAddr("ip4", server.IP)
	if err != nil {
		log.Printf("%sServer %s (%s) - Resolve error: %v", logPrefix, server.Name, server.IP, err)
		setServerStatus(server, "error", mu)
		return
	}

	netconn, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		log.Printf("%sServer %s (%s) - ICMP listen error: %v", logPrefix, server.Name, server.IP, err)
		setServerStatus(server, "error", mu)
		return
	}
	defer netconn.Close()

	var isAlive bool
	for attempt := 0; attempt < maxPingAttempts; attempt++ {
		log.Printf("%sServer %s (%s) - Attempt %d/%d (ID: %d)", logPrefix, server.Name, server.IP, attempt+1, maxPingAttempts, server.ICMPID)

		isAlive = pingAttempt(netconn, ipAddr, timeout, server)
		if isAlive {
			log.Printf("%sServer %s (%s) - PING SUCCESS after attempt %d", logPrefix, server.Name, server.IP, attempt+1)
			break
		}
		time.Sleep(timeout / time.Duration(maxPingAttempts))
	}

	mu.Lock()
	defer mu.Unlock()

	if isAlive {
		server.Failures = 0
		server.Status = "alive"
	} else {
		server.Failures++
		if server.Failures >= 3 {
			server.Status = "dead"
		}
	}
	server.LastPing = time.Now()
	log.Printf("%sServer %s (%s) - Status: %s (Failures: %d)", logPrefix, server.Name, server.IP, server.Status, server.Failures)
}

func pingAttempt(netconn net.PacketConn, ipAddr *net.IPAddr, timeout time.Duration, server *models.Server) bool {
	err := netconn.SetReadDeadline(time.Now().Add(timeout))
	if err != nil {
		log.Printf("%sServer %s (%s) - Set read deadline error: %v", logPrefix, server.Name, server.IP, err)
		return false
	}

	seq := time.Now().Nanosecond() & 0xffff
	id := server.ICMPID

	wm := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   id,
			Seq:  seq,
			Data: []byte("ping"),
		},
	}

	wb, err := wm.Marshal(nil)
	if err != nil {
		log.Printf("%sServer %s (%s) - ICMP marshal error: %v", logPrefix, server.Name, server.IP, err)
		return false
	}

	if _, err := netconn.WriteTo(wb, ipAddr); err != nil {
		log.Printf("%sServer %s (%s) - ICMP write error: %v", logPrefix, server.Name, server.IP, err)
		return false
	}

	rb := make([]byte, 1500)
	n, peer, err := netconn.ReadFrom(rb)
	if err != nil {
		log.Printf("%sServer %s (%s) - Read error: %v", logPrefix, server.Name, server.IP, err)
		return false
	}

	rm, err := icmp.ParseMessage(1, rb[:n])
	if err != nil {
		log.Printf("%sServer %s (%s) - Parse error: %v", logPrefix, server.Name, server.IP, err)
		return false
	}

	switch body := rm.Body.(type) {
	case *icmp.Echo:
		if body.ID == id && body.Seq == seq {
			log.Printf("%sServer %s (%s) - Valid ICMP reply from %s", logPrefix, server.Name, server.IP, peer.String())
			return true
		}
		log.Printf("%sServer %s (%s) - Mismatched ICMP reply: got id=%d seq=%d, expected id=%d seq=%d",
			logPrefix, server.Name, server.IP, body.ID, body.Seq, id, seq)
	}
	return false
}

func setServerStatus(server *models.Server, status string, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	server.Status = status
}

func MonitorServers(servers *[]models.Server, interval time.Duration, timeout time.Duration, mu *sync.Mutex, ctx context.Context) {
	var wg sync.WaitGroup
	for i := range *servers {
		if (*servers)[i].IP != "" {
			wg.Add(1)
			go monitorSingleServer(ctx, &(*servers)[i], interval, timeout, mu, &wg)
		}
	}
	wg.Wait()
}

func monitorSingleServer(ctx context.Context, server *models.Server, interval time.Duration, timeout time.Duration, mu *sync.Mutex, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("Monitoring stopped for server %s", server.IP)
			return
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("Recovered from panic: %v", r)
					}
				}()
				PingServer(server, wg, mu, timeout)
			}()
		}
	}
}

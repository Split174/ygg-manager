package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/pflag"
)

const (
	PeersJSONURL = "https://raw.githubusercontent.com/Yggdrasil-Unofficial/pubpeers/refs/heads/master/peers.json"
	PingTimeout  = 3 * time.Second
	BatchSize    = 20
	TopNEntropy  = 7

	CheckInterval      = 30 * time.Second
	MaxStrikes         = 5
	MaxConcurrentPings = 5
	SlowStartDelay     = 2 * time.Second
)

var (
	maxPeers      int
	maxLatency    time.Duration
	maxCost       float64
	targetCountry string

	peerStrikes = make(map[string]int)
)

type YggRequest struct {
	Request   string                 `json:"request"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

type PeerStat struct {
	URI     string
	Host    string
	Latency time.Duration
}

type ActivePeer struct {
	URI  string
	Cost float64
	IsUp bool
}

func main() {
	defEndpoint := findDefaultEndpoint()
	if envEndpoint := os.Getenv("YGG_ENDPOINT"); envEndpoint != "" {
		defEndpoint = envEndpoint
	}

	defMaxPeers := 3
	if val := os.Getenv("MAX_PEERS"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			defMaxPeers = parsed
		}
	}

	defLatencyMs := 150
	if val := os.Getenv("MAX_LATENCY_MS"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			defLatencyMs = parsed
		}
	}

	defMaxCost := 250.0
	if val := os.Getenv("MAX_COST"); val != "" {
		if parsed, err := strconv.ParseFloat(val, 64); err == nil {
			defMaxCost = parsed
		}
	}

	defCountry := os.Getenv("PEER_COUNTRY")

	var (
		argEndpoint string
		argMaxPeers int
		argLatency  int
		argMaxCost  float64
		argCountry  string
		showHelp    bool
	)

	pflag.StringVarP(&argEndpoint, "endpoint", "e", defEndpoint, "Yggdrasil API endpoint (env: YGG_ENDPOINT)")
	pflag.IntVarP(&argMaxPeers, "max-peers", "p", defMaxPeers, "Maximum number of peers (env: MAX_PEERS)")
	pflag.IntVarP(&argLatency, "max-latency", "l", defLatencyMs, "Maximum latency in ms (env: MAX_LATENCY_MS)")
	pflag.Float64VarP(&argMaxCost, "max-cost", "c", defMaxCost, "Maximum cost of peer (env: MAX_COST)")
	pflag.StringVar(&argCountry, "country", defCountry, "Target country/region for peers e.g., 'netherlands' (env: PEER_COUNTRY)")
	pflag.BoolVarP(&showHelp, "help", "h", false, "Show this help message")

	pflag.Parse()

	if showHelp {
		fmt.Println("Yggdrasil Smart Peer Manager")
		fmt.Println("Usage:")
		pflag.PrintDefaults()
		os.Exit(0)
	}

	endpoint := argEndpoint

	if argMaxPeers > 4 {
		log.Printf("[WARNING] max-peers = %d specified. Limit exceeded! Forcing to: 4", argMaxPeers)
		maxPeers = 4
	} else if argMaxPeers <= 0 {
		maxPeers = 3
	} else {
		maxPeers = argMaxPeers
	}

	if argLatency < 100 {
		log.Printf("[WARNING] max-latency = %d specified. Too aggressive ping! Forcing to: 100", argLatency)
		maxLatency = 100 * time.Millisecond
	} else {
		maxLatency = time.Duration(argLatency) * time.Millisecond
	}

	if argMaxCost < 150.0 {
		log.Printf("[WARNING] max-cost = %.1f specified. Too low cost! Forcing to: 150.0", argMaxCost)
		maxCost = 150.0
	} else {
		maxCost = argMaxCost
	}

	targetCountry = strings.ToLower(strings.TrimSpace(argCountry))

	log.Println("Yggdrasil Smart Peer Manager started...")
	log.Printf("Endpoint: %s", endpoint)
	log.Printf("Max Peers: %d", maxPeers)
	log.Printf("Max Ping: %v", maxLatency)
	log.Printf("Max Cost: %.1f", maxCost)
	log.Printf("Country : %s (if empty - worldwide)", targetCountry)

	rand.Seed(time.Now().UnixNano())

	for {
		managePeers(endpoint)
		log.Printf("Sleeping for %v...\n\n", CheckInterval)
		time.Sleep(CheckInterval)
	}
}

func findDefaultEndpoint() string {
	candidatePaths := []string{
		"/var/run/yggdrasil.sock",
		"/var/run/yggdrasil/yggdrasil.sock",
		"/run/yggdrasil.sock",
		"/run/yggdrasil/yggdrasil.sock",
	}

	for _, path := range candidatePaths {
		if _, err := os.Stat(path); err == nil {
			return "unix://" + path
		}
	}

	return "unix:///var/run/yggdrasil.sock"
}

func managePeers(endpoint string) {
	currentPeers, err := getCurrentPeers(endpoint)
	if err != nil {
		log.Printf("Error getting current peers: %v", err)
		return
	}

	activeCount := len(currentPeers)
	log.Printf("Current outbound peers in Yggdrasil (incl. Down): %d / %d", activeCount, maxPeers)

	connectedHosts := make(map[string]bool)
	activeHostsThisRun := make(map[string]bool)

	for _, peer := range currentPeers {
		host := extractHost(peer.URI)
		latency := checkLatency(peer.URI)
		activeHostsThisRun[host] = true

		if !peer.IsUp || latency == 0 || latency > maxLatency || peer.Cost > maxCost {
			var reason string
			if !peer.IsUp {
				reason = "dropped inside Yggdrasil (Down/i-o timeout)"
			} else if latency == 0 {
				reason = "physically dead (TCP ping 0)"
			} else if latency > maxLatency {
				reason = fmt.Sprintf("too slow (%v > %v)", latency, maxLatency)
			} else {
				reason = fmt.Sprintf("cost too high (%.0f > %.0f)", peer.Cost, maxCost)
			}

			peerStrikes[host]++
			log.Printf("[~] WARNING: Peer %s %s. Strike given: %d/%d", host, reason, peerStrikes[host], MaxStrikes)

			if peerStrikes[host] >= MaxStrikes {
				log.Printf("[-] Peer %s exceeded strike limit. Removing permanently...", host)
				removePeer(endpoint, peer.URI)
				activeCount--
				delete(peerStrikes, host)
			} else {
				connectedHosts[host] = true
			}
		} else {
			peerStrikes[host] = 0
			log.Printf("[OK] Current peer %s is stable. Ping: %v | Cost: %.0f", host, latency, peer.Cost)
			connectedHosts[host] = true
		}
	}

	for host := range peerStrikes {
		if !activeHostsThisRun[host] {
			delete(peerStrikes, host)
		}
	}

	if activeCount >= maxPeers {
		log.Println("Peer count is normal. No search required.")
		return
	}

	log.Printf("Slots freed: %d. Searching for new ones...", maxPeers-activeCount)

	countryPeers, globalPeers, err := fetchAndSplitPeers(PeersJSONURL, targetCountry)
	if err != nil {
		log.Printf("Error fetching node list: %v", err)
		return
	}

	testBatch := buildTestBatch(countryPeers, globalPeers, BatchSize)

	var scoredPeers []PeerStat
	var mu sync.Mutex
	var wg sync.WaitGroup

	sem := make(chan struct{}, MaxConcurrentPings)

	log.Printf("Pinging %d new nodes concurrently (up to %d threads)...", len(testBatch), MaxConcurrentPings)

	for _, uri := range testBatch {
		host := extractHost(uri)
		if connectedHosts[host] {
			continue
		}

		wg.Add(1)
		sem <- struct{}{}

		go func(nodeUri, nodeHost string) {
			defer wg.Done()
			defer func() { <-sem }()

			lat := checkLatency(nodeUri)
			if lat > 0 && lat <= maxLatency {
				mu.Lock()
				scoredPeers = append(scoredPeers, PeerStat{URI: nodeUri, Host: nodeHost, Latency: lat})
				mu.Unlock()
			}
		}(uri, host)
	}

	wg.Wait()

	if len(scoredPeers) == 0 {
		log.Println("No new peers passed the preliminary TCP check.")
		return
	}

	sort.Slice(scoredPeers, func(i, j int) bool {
		return scoredPeers[i].Latency < scoredPeers[j].Latency
	})

	if len(scoredPeers) > TopNEntropy {
		scoredPeers = scoredPeers[:TopNEntropy]
	}

	rand.Shuffle(len(scoredPeers), func(i, j int) {
		scoredPeers[i], scoredPeers[j] = scoredPeers[j], scoredPeers[i]
	})

	for _, p := range scoredPeers {
		if activeCount >= maxPeers {
			break
		}
		if connectedHosts[p.Host] {
			continue
		}

		log.Printf("[+] Connecting peer: %s (Expected ping: %v)", p.URI, p.Latency)
		err := addPeer(endpoint, p.URI)
		if err == nil {
			activeCount++
			connectedHosts[p.Host] = true
			peerStrikes[p.Host] = 0

			if activeCount < maxPeers {
				log.Printf("⏳ Giving Yggdrasil %v to stabilize the connection...", SlowStartDelay)
				time.Sleep(SlowStartDelay)
			}
		}
	}
}

func callYggAPI(endpoint string, req YggRequest) (map[string]interface{}, error) {
	parts := strings.SplitN(endpoint, "://", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("bad endpoint format: %s", endpoint)
	}
	network, addr := parts[0], parts[1]

	conn, err := net.Dial(network, addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}

	var response map[string]interface{}
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return nil, err
	}
	return response, nil
}

func getCurrentPeers(endpoint string) ([]ActivePeer, error) {
	resp, err := callYggAPI(endpoint, YggRequest{Request: "getPeers"})
	if err != nil {
		return nil, err
	}

	var activePeers []ActivePeer
	var peersList []interface{}

	if responseObj, ok := resp["response"].(map[string]interface{}); ok {
		if pList, ok := responseObj["peers"].([]interface{}); ok {
			peersList = pList
		}
	} else if pList, ok := resp["peers"].([]interface{}); ok {
		peersList = pList
	}

	if peersList == nil {
		return activePeers, nil
	}

	for _, p := range peersList {
		peerData, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if inbound, ok := peerData["inbound"].(bool); ok && inbound {
			continue
		}

		isUp := false
		if upStatus, ok := peerData["up"].(bool); ok {
			isUp = upStatus
		}

		uri := ""
		if rem, ok := peerData["remote"].(string); ok {
			uri = rem
		} else if ep, ok := peerData["endpoint"].(string); ok {
			uri = ep
		}

		cost := 0.0
		if c, ok := peerData["cost"].(float64); ok {
			cost = c
		} else if cInt, ok := peerData["cost"].(int); ok {
			cost = float64(cInt)
		}

		if uri != "" {
			activePeers = append(activePeers, ActivePeer{
				URI:  uri,
				Cost: cost,
				IsUp: isUp,
			})
		}
	}
	return activePeers, nil
}

func addPeer(endpoint, uri string) error {
	req := YggRequest{
		Request: "addPeer",
		Arguments: map[string]interface{}{
			"uri": uri,
		},
	}
	_, err := callYggAPI(endpoint, req)
	return err
}

func removePeer(endpoint, uri string) error {
	req := YggRequest{
		Request: "removePeer",
		Arguments: map[string]interface{}{
			"uri": uri,
		},
	}
	_, err := callYggAPI(endpoint, req)
	return err
}

func fetchAndSplitPeers(jsonURL, target string) ([]string, []string, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequest("GET", jsonURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "Yggdrasil-Smart-Peer-Manager/1.1 (https://github.com/your-username/yggdrasil-manager)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}

	var rawData map[string]interface{}
	if err := json.Unmarshal(body, &rawData); err != nil {
		return nil, nil, err
	}

	var countryList []string
	var globalList []string

	for countryKey, nodes := range rawData {
		uris := extractOnlyTcpTls(nodes)
		if target != "" && strings.ToLower(countryKey) == target {
			countryList = append(countryList, uris...)
		} else {
			globalList = append(globalList, uris...)
		}
	}

	return countryList, globalList, nil
}

func extractOnlyTcpTls(data interface{}) []string {
	var result []string
	switch v := data.(type) {
	case string:
		if strings.HasPrefix(v, "tcp://") || strings.HasPrefix(v, "tls://") {
			result = append(result, v)
		}
	case map[string]interface{}:
		for _, val := range v {
			result = append(result, extractOnlyTcpTls(val)...)
		}
	case []interface{}:
		for _, val := range v {
			result = append(result, extractOnlyTcpTls(val)...)
		}
	}
	return result
}

func buildTestBatch(country, global []string, needed int) []string {
	rand.Shuffle(len(country), func(i, j int) { country[i], country[j] = country[j], country[i] })
	rand.Shuffle(len(global), func(i, j int) { global[i], global[j] = global[j], global[i] })

	var batch []string

	takeCountry := needed
	if len(country) < takeCountry {
		takeCountry = len(country)
	}
	batch = append(batch, country[:takeCountry]...)

	rem := needed - len(batch)
	if rem > 0 && len(global) > 0 {
		takeGlobal := rem
		if len(global) < takeGlobal {
			takeGlobal = len(global)
		}
		batch = append(batch, global[:takeGlobal]...)
	}

	return batch
}

func extractHost(uriStr string) string {
	u, err := url.Parse(uriStr)
	if err != nil {
		return uriStr
	}
	return u.Hostname()
}

func checkLatency(uriStr string) time.Duration {
	u, err := url.Parse(uriStr)
	if err != nil {
		return 0
	}

	network := "tcp"
	if u.Scheme == "quic" {
		network = "udp"
	}

	start := time.Now()
	conn, err := net.DialTimeout(network, u.Host, PingTimeout)
	if err != nil {
		return 0
	}
	conn.Close()
	return time.Since(start)
}

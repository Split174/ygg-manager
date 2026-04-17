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
	"time"
)

const (
	DefaultEndpoint   = "unix:///var/run/yggdrasil/yggdrasil.sock"
	PeersJSONURL      = "https://raw.githubusercontent.com/Yggdrasil-Unofficial/pubpeers/refs/heads/master/peers.json"
	MaxPeers          = 3
	PingTimeout       = 3 * time.Second
	CheckInterval     = 2 * time.Minute
	DefaultMaxLatency = 300
	BatchSize         = 20
)

var (
	maxLatency    time.Duration
	targetCountry string
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

func main() {
	log.Println("Yggdrasil Smart Peer Manager запущен...")

	endpoint := os.Getenv("YGG_ENDPOINT")
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}

	maxLatency = time.Duration(DefaultMaxLatency) * time.Millisecond
	if val := os.Getenv("MAX_LATENCY_MS"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			maxLatency = time.Duration(parsed) * time.Millisecond
		}
	}

	targetCountry = strings.ToLower(strings.TrimSpace(os.Getenv("PEER_COUNTRY")))

	log.Printf("Endpoint: %s", endpoint)
	log.Printf("Max Ping: %v", maxLatency)
	log.Printf("Country : %s (если пусто - весь мир)", targetCountry)

	rand.Seed(time.Now().UnixNano())

	for {
		managePeers(endpoint)
		log.Printf("Спим %v...\n\n", CheckInterval)
		time.Sleep(CheckInterval)
	}
}

func managePeers(endpoint string) {
	currentPeers, err := getCurrentPeers(endpoint)
	if err != nil {
		log.Printf("Ошибка получения текущих пиров: %v", err)
		return
	}

	activeCount := len(currentPeers)
	log.Printf("Текущих активных исходящих пиров: %d / %d", activeCount, MaxPeers)

	connectedHosts := make(map[string]bool)

	for _, uri := range currentPeers {
		host := extractHost(uri)
		latency := checkLatency(uri)

		if latency == 0 || latency > maxLatency {
			log.Printf("[-] Пир %s умер или медленный (%v > %v). Удаляем...", host, latency, maxLatency)
			removePeer(endpoint, uri)
			activeCount--
		} else {
			log.Printf("[OK] Текущий пир %s жив. Пинг: %v", host, latency)
			connectedHosts[host] = true
		}
	}

	if activeCount >= MaxPeers {
		log.Println("Количество пиров в норме.")
		return
	}

	log.Printf("Не хватает %d пиров. Ищем новые...", MaxPeers-activeCount)

	countryPeers, globalPeers, err := fetchAndSplitPeers(PeersJSONURL, targetCountry)
	if err != nil {
		log.Printf("Ошибка получения списка: %v", err)
		return
	}

	testBatch := buildTestBatch(countryPeers, globalPeers, BatchSize)
	var scoredPeers []PeerStat

	log.Printf("Выбрано %d нод для пинга...", len(testBatch))

	for _, uri := range testBatch {
		host := extractHost(uri)
		if connectedHosts[host] {
			continue
		}

		lat := checkLatency(uri)
		if lat > 0 {
			scoredPeers = append(scoredPeers, PeerStat{URI: uri, Host: host, Latency: lat})
		}
	}

	sort.Slice(scoredPeers, func(i, j int) bool {
		return scoredPeers[i].Latency < scoredPeers[j].Latency
	})

	for _, p := range scoredPeers {
		if activeCount >= MaxPeers {
			break
		}
		if connectedHosts[p.Host] {
			continue
		}
		if p.Latency > maxLatency {
			continue
		}

		log.Printf("[+] Добавляем пира: %s (Пинг: %v)", p.URI, p.Latency)
		err := addPeer(endpoint, p.URI)
		if err == nil {
			activeCount++
			connectedHosts[p.Host] = true
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

func getCurrentPeers(endpoint string) ([]string, error) {
	resp, err := callYggAPI(endpoint, YggRequest{Request: "getPeers"})
	if err != nil {
		return nil, err
	}

	var activeURIs []string
	var peersList []interface{}

	if responseObj, ok := resp["response"].(map[string]interface{}); ok {
		if pList, ok := responseObj["peers"].([]interface{}); ok {
			peersList = pList
		}
	} else if pList, ok := resp["peers"].([]interface{}); ok {
		peersList = pList
	}

	if peersList == nil {
		return activeURIs, nil
	}

	for _, p := range peersList {
		peerData, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		if inbound, ok := peerData["inbound"].(bool); ok && inbound {
			continue
		}
		if up, ok := peerData["up"].(bool); ok && !up {
			continue
		}

		uri := ""
		if rem, ok := peerData["remote"].(string); ok {
			uri = rem
		} else if ep, ok := peerData["endpoint"].(string); ok {
			uri = ep
		}

		if uri != "" {
			activeURIs = append(activeURIs, uri)
		}
	}
	return activeURIs, nil
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
	resp, err := http.Get(jsonURL)
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

	start := time.Now()
	conn, err := net.DialTimeout("tcp", u.Host, PingTimeout)
	if err != nil {
		return 0
	}
	conn.Close()
	return time.Since(start)
}

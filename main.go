package main

import (
	"flag"
	"log"
	"math/rand"
	"net"
	"sync"
	"time"
	"fmt"
	"net/http"
	"io/ioutil"
	"encoding/json"
	"os"
)

var (
	stateWorldMutex = sync.RWMutex{}
	stateWorld = make(map[string]Data, 0)
)

type Data struct {
	Value   string
	Version int
}

type Message struct {
	Address []string
	DataSet map[string]Data
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func randomChoice(items []string, n int) []string {
	if len(items) == 0 {
		return []string{}
	}
	if len(items) < n {
		n = len(items)
	}
	seen := make(map[uint32]bool)
	ret := make([]string, 0, n)
	for len(seen) < n {
		idx := rand.Uint32() % uint32(len(items))
		if _, ok := seen[idx]; !ok {
			seen[idx] = true
			ret = append(ret, items[idx])
		}
	}
	return ret
}

type setRes struct {
	Key     string
	Version int
	Value   string
	Status  string
}

type setReq struct {
	Key   string
	Value string
}

type getRes struct {
	Key     string
	Version int
	Value   string
	Status  string
}

type getReq struct {
	Key string
}

func handleGet(w http.ResponseWriter, r *http.Request) {
	var (
		res getRes
		req getReq
	)
	res.Status = "error"
	defer func() {
		r, err := json.Marshal(&res)
		if err != nil {
			log.Println(err)
		}
		w.Write(r)
	}()
	reqBody, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return
	}
	err = json.Unmarshal(reqBody, &req)
	if err != nil {
		return
	}
	if d, exist := stateWorld[req.Key]; exist {
		res.Key = req.Key
		res.Version = d.Version
		res.Value = d.Value
	}
	res.Status = "success"
}

func handleSet(w http.ResponseWriter, r *http.Request) {
	var (
		res setRes
		req setReq
	)
	res.Status = "error"
	defer func() {
		r, err := json.Marshal(&res)
		if err != nil {
			log.Println(err)
		}
		w.Write(r)
	}()
	reqBody, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return
	}
	err = json.Unmarshal(reqBody, &req)
	if err != nil {
		return
	}
	stateWorldMutex.Lock()
	defer stateWorldMutex.Unlock()
	if d, exist := stateWorld[req.Key]; exist {
		if d.Value != req.Value {
			d.Version = d.Version + 1
			d.Value = req.Value
		}
		stateWorld[req.Key] = d
		res.Value = d.Value
		res.Key = req.Key
		res.Version = d.Version
	} else {
		stateWorld[req.Key] = Data{Value:req.Value, Version:0}
		res.Value = req.Value
		res.Key = req.Key
		res.Version = 0
	}
	res.Status = "success"
}

func startServer() {
	http.HandleFunc("/get", handleGet)
	http.HandleFunc("/set", handleSet)
	http.ListenAndServe(fmt.Sprintf(":%v", 8080), nil)
}

func getLocalIp() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		os.Exit(1)
	}
	for _, address := range addrs {
		// 检查ip地址判断是否回环地址
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	log.Fatal("Find no local ip")
	return ""
}

func main() {
	port := flag.Int("port", 10001, "listen port updates on this address")
	listenAddr := fmt.Sprintf("%s:%d", getLocalIp(), *port)
	fanout := flag.Uint("fanout", 2, "fanout of the gossip protocol")
	periodSeconds := flag.Duration("period", 3, "gossip period in seconds")

	flag.Parse()
	addr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		log.Fatal("Error resolving UDP address for self: " + err.Error())
	}

	log.Printf("Resolved address to %v", addr)

	srvConn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatal("Error listening: " + err.Error())
	}
	defer srvConn.Close()

	var membersMutex sync.RWMutex
	members := map[string]bool{
		listenAddr: true,
	}

	stop := make(chan struct{})
	go startServer()
	go func(stop chan struct{}) {
		//執行一次廣播
		laddr := net.UDPAddr{
			IP:   net.ParseIP(getLocalIp()),
			Port: 9090,
		}
		// 这里设置接收者的IP地址为广播地址
		raddr := net.UDPAddr{
			IP:   net.IPv4(10, 20, 19, 255),
			Port: *port,
		}
		conn, err := net.DialUDP("udp", &laddr, &raddr)
		if err != nil {
			log.Fatal("Broadcast failed.")
		}
		message := Message{Address: []string{listenAddr}}
		bs, _ := json.Marshal(&message)
		conn.Write(bs)
		conn.Close()

		log.Printf("==== Do broadcast message ===")

		buf := make([]byte, 1024)
		for {
			select {
			case <-stop:
				break
			default:
				n, fromAddr, err := srvConn.ReadFromUDP(buf)
				if n > 0 {
					var mes Message
					err = json.Unmarshal(buf, &mes)
					if err != nil {
						fmt.Printf("Failed to unmarshal message")
					}
					addresses := mes.Address
					log.Printf("==== Do receive message: %v === ", addresses)
					log.Printf("Received %d addresses from %v: %v", len(addresses), fromAddr.IP, addresses)
					membersMutex.Lock()
					for _, address := range addresses {
						members[address] = true
					}
					membersMutex.Unlock()
					stateWorldMutex.Lock()
					for k, v := range mes.DataSet {
						if oldData, exist := stateWorld[k]; exist {
							if oldData.Version < v.Version {
								stateWorld[k] = v
							}
						} else {
							stateWorld[k] = v
						}
					}
					stateWorldMutex.Unlock()
				}
				if err != nil {
					log.Fatalf("Receive error on UDP connection: %v", err.Error())
				}
			}
		}
	}(stop)

	defer func() {
		stop <- struct{}{}
	}()

	// Every periodSeconds seconds, contact `fanout` random members from our list
	// with our membership list.
	ticker := time.NewTicker(time.Second * *periodSeconds)
	defer ticker.Stop()
	for _ = range ticker.C {

		membersMutex.RLock()
		nMembers := len(members)
		// Holds addresses of members to contact
		peerAddresses := make([]string, 0, nMembers - 1)
		addrsToSend := make([]string, 0, nMembers)
		for a, _ := range members {
			if a != listenAddr {
				peerAddresses = append(peerAddresses, a)
			}
			addrsToSend = append(addrsToSend, a)
		}
		membersMutex.RUnlock()

		log.Printf("tick: we know about %d (including us): %v", len(addrsToSend), addrsToSend)
		log.Printf("going to poke %d of %d peers", minInt(int(len(peerAddresses)), int(*fanout)), len(peerAddresses))

		chosen := randomChoice(peerAddresses, int(*fanout))
		log.Printf("found these: %v", chosen)

		// stick our address in the message
		for _, addrStr := range chosen {
			theirAddr, err := net.ResolveUDPAddr("udp", addrStr)
			if err != nil {
				log.Printf("Could NOT resolve %v as a valid UDP address, continuing", addrStr)
				continue
			}
			log.Printf("Going to talk to %v", theirAddr)

			conn, err := net.DialUDP("udp", nil, theirAddr)
			if err != nil {
				log.Printf("Error dialing %v: %v, continuing", theirAddr, err.Error())
				continue
			}
			sendMes := Message{Address:addrsToSend, DataSet:stateWorld}
			messageBytes, err := json.Marshal(&sendMes)
			if err != nil {
				fmt.Printf("Marshal message failed.")
				continue
			}
			_, err = conn.Write(messageBytes)
			if err != nil {
				membersMutex.RLock()
				delete(members, addrStr)
				membersMutex.RUnlock()
				log.Printf("Error sending message to  %s: %s, continuing", addrStr, err.Error())
				continue
			}
			conn.Close()
		}
	}
}
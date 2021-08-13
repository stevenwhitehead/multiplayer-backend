package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-redis/redis"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type RedisConnection struct {
	rediss Rediss `json:"rediss"`
}

type Rediss struct {
	Composed []string `json:"composed"`
}

type Input struct {
	id     string
	Inputs []string `json:"inputs"`
}

type GameState map[string]*Player

type Player struct {
	up    bool
	down  bool
	left  bool
	right bool
	X     int `json:"x"`
	Y     int `json:"y"`
}

var rdb *redis.Client
var gamestate GameState
var sockets map[string]*websocket.Conn
var tick = 24 * time.Millisecond

var eventQueue = []Input{}
var eventLock = sync.Mutex{}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func main() {
	redisEnv := os.Getenv("DATABASES_FOR_REDIS_CONNECTION")

	var redisCon RedisConnection
	err := json.Unmarshal([]byte(redisEnv), &redisCon)
	if err != nil {
		fmt.Println("redis connection error", err.Error())
		return
	}
	opts, err := redis.ParseURL(redisCon.rediss.Composed[0]) // TODO index check
	if err != nil {
		fmt.Println("redis parse error", err.Error())
		return
	}
	rdb = redis.NewClient(opts)
	gamestate = GameState{}
	sockets = map[string]*websocket.Conn{}

	pubsub := rdb.Subscribe("mychannel")
	defer pubsub.Close()

	// goroutine for retrieving events from redis and adding to event queue
	go func() {
		for {
			msg, err := pubsub.ReceiveMessage()
			if err != nil {
				fmt.Println("pubsub error:", err.Error())
				continue
			}
			eventLock.Lock()
			var input Input
			err = json.Unmarshal([]byte(msg.Payload), &input)
			if err != nil {
				fmt.Println("unmarshal error:", err.Error())
				continue
			}
			eventQueue = append(eventQueue, input)
			eventLock.Unlock()
		}
	}()

	// go func for processing eventqueue and sending gamestate
	go func() {
		ticker := time.NewTicker(tick)
		for {
			<-ticker.C
			eventLock.Lock()

			for k := range gamestate {
				gamestate[k].left = false
				gamestate[k].right = false
				gamestate[k].up = false
				gamestate[k].down = false
			}

			for _, input := range eventQueue {
				for _, str := range input.Inputs {
					switch str {
					case "left":
						gamestate[input.id].left = true
					case "right":
						gamestate[input.id].right = true
					case "up":
						gamestate[input.id].up = true
					case "down":
						gamestate[input.id].down = true
					}
				}
			}
			for k := range gamestate {
				p := gamestate[k]
				if p.left {
					p.X -= 1
				}
				if p.right {
					p.X += 1
				}
				if p.up {
					p.Y -= 1
				}
				if p.down {
					p.Y += 1
				}

				// clamp values
				p.X = int(math.Max(0, float64(p.X)))
				p.X = int(math.Min(800, float64(p.X)))
				p.Y = int(math.Max(0, float64(p.Y)))
				p.Y = int(math.Min(600, float64(p.Y)))

			}
			eventQueue = []Input{}
			eventLock.Unlock()

			for _, s := range sockets {
				err := s.WriteJSON(gamestate)
				if err != nil {
					log.Println("err:", err)
					return
				}
			}
		}
	}()

	http.HandleFunc("/", home)
	http.HandleFunc("/game", game)
	http.ListenAndServe(":8080", nil)
}

func home(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("ok"))
}

func game(w http.ResponseWriter, r *http.Request) {
	log.Println("user connected:", r.URL.User)
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade:", err)
		return
	}
	defer c.Close()
	log.Println("websocket upgrade:", c.LocalAddr().String())

	id := uuid.New().String()
	sockets[id] = c
	gamestate[id] = &Player{
		X: 400,
		Y: 300,
	}
	defer func() {
		eventLock.Lock()
		delete(gamestate, id)
		delete(sockets, id)
		eventLock.Unlock()
	}()

	for {
		_, message, err := c.ReadMessage()
		if err != nil {
			log.Println("read:", err)
			return
		}
		var input Input
		err = json.Unmarshal(message, &input.Inputs)
		input.id = id
		if err != nil {
			log.Printf("err: %s", err.Error())
			return
		}
		rdb.Publish("channel", input)
	}
}

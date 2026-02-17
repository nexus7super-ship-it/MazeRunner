// MIT License

// Copyright (c) 2026 nexus7super-ship-it

// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:

// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.

// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

type Player struct {
	X          int    `json:"x"`
	Y          int    `json:"y"`
	Name       string `json:"name"`
	Color      string `json:"color"`
	Finished   bool   `json:"finished"`
	FinishTime int64  `json:"finishTime"`
	FinishRank int    `json:"finishRank"`
}

type GameState struct {
	AllFinished bool     `json:"allFinished"`
	Players     []Player `json:"players"`
	GameOver    bool     `json:"gameOver"`
}

type MazeInfo struct {
	GoalX  int `json:"goalX"`
	GoalY  int `json:"goalY"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

var (
	maze       [][]int
	mazeWidth  = 71
	mazeHeight = 41
	goalX      = 69
	goalY      = 39
	clients    = make(map[*websocket.Conn]*Player)
	mu         sync.Mutex
	finishRank = 0
	gameOver   = false
	startTime  time.Time
)

func generateMaze() {
	h, w := mazeHeight, mazeWidth
	log.Printf("Generating maze %dx%d...", w, h)
	maze = make([][]int, h)
	for y := range maze {
		maze[y] = make([]int, w)
		for x := range maze[y] {
			maze[y][x] = 1
		}
	}
	rand.Seed(time.Now().UnixNano())
	var walk func(x, y int)
	walk = func(x, y int) {
		maze[y][x] = 0
		dirs := [][2]int{{0, 2}, {0, -2}, {2, 0}, {-2, 0}}
		rand.Shuffle(len(dirs), func(i, j int) { dirs[i], dirs[j] = dirs[j], dirs[i] })
		for _, d := range dirs {
			nx, ny := x+d[0], y+d[1]
			if nx > 0 && nx < w-1 && ny > 0 && ny < h-1 && maze[ny][nx] == 1 {
				maze[y+d[1]/2][x+d[0]/2] = 0
				walk(nx, ny)
			}
		}
	}
	walk(1, 1)
	goalX = w - 2
	goalY = h - 2
	// Make sure goal is even (reachable by maze generator)
	if goalX%2 == 0 {
		goalX--
	}
	if goalY%2 == 0 {
		goalY--
	}
	maze[goalY][goalX] = 0
	log.Printf("Maze generated. Goal at (%d, %d)", goalX, goalY)
}

func broadcast() {
	mu.Lock()
	defer mu.Unlock()

	var list []Player
	allDone := true
	playerCount := len(clients)

	for _, p := range clients {
		list = append(list, *p)
		if !p.Finished {
			allDone = false
		}
	}

	if allDone && playerCount > 0 && !gameOver {
		gameOver = true
		log.Println("GAME OVER: All players have reached the goal!")
	}

	state := GameState{
		AllFinished: allDone && playerCount > 0,
		Players:     list,
		GameOver:    gameOver,
	}

	data, _ := json.Marshal(state)
	for conn := range clients {
		if err := websocket.Message.Send(conn, string(data)); err != nil {
			// Don't log every write error
		}
	}
}

func handleWS(ws *websocket.Conn) {
	startTimeConnection := time.Now()
	remoteAddr := ws.Request().RemoteAddr
	log.Printf("New connection from %s", remoteAddr)
	
	p := &Player{X: 1, Y: 1, Name: "Anon", Color: "#ff0000"}

	mu.Lock()
	clients[ws] = p
	mu.Unlock()

	broadcast()

	defer func() {
		mu.Lock()
		delete(clients, ws)
		mu.Unlock()
		ws.Close()
		broadcast()
		duration := time.Since(startTimeConnection)
		log.Printf("Connection closed (duration: %v): %s [%s]", duration, remoteAddr, p.Name)
	}()

	for {
		var msg Player
		if err := websocket.JSON.Receive(ws, &msg); err != nil {
			if err != io.EOF {
				log.Printf("Read error from %s: %v", remoteAddr, err)
			}
			break
		}

		mu.Lock()
		wasFinished := p.Finished
		p.X, p.Y, p.Name, p.Color = msg.X, msg.Y, msg.Name, msg.Color

		if msg.Finished && !wasFinished {
			p.Finished = true
			finishRank++
			p.FinishRank = finishRank
			p.FinishTime = time.Now().Unix() - startTime.Unix()
			log.Printf("PLAYER FINISHED! Name: %s | Rank: %d | Time: %ds", p.Name, p.FinishRank, p.FinishTime)
		}
		mu.Unlock()

		broadcast()
	}
}

func resetGame() {
	log.Println("Game reset requested via API")
	mu.Lock()
	finishRank = 0
	gameOver = false
	for _, p := range clients {
		p.X = 1
		p.Y = 1
		p.Finished = false
		p.FinishRank = 0
		p.FinishTime = 0
	}
	mu.Unlock()
	generateMaze()
	startTime = time.Now()
	broadcast()
}

func readLine(reader *bufio.Reader) string {
	line, _ := reader.ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	return strings.TrimSpace(line)
}

func setupGameHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/maze", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(maze)
	})
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		json.NewEncoder(w).Encode(MazeInfo{GoalX: goalX, GoalY: goalY, Width: mazeWidth, Height: mazeHeight})
	})
	mux.Handle("/ws", websocket.Handler(handleWS))
	mux.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		resetGame()
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
}

func setupWebsiteHandlers(mux *http.ServeMux, gamePort string) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Inject the game port if it differs, or if we want to be explicit
		// We replace the placeholder <!--SERVER_CONFIG--> with a small script
		configScript := ""
		if gamePort != "" {
			configScript = fmt.Sprintf("<script>window.DEFAULT_GAME_PORT='%s';</script>", gamePort)
		}
		
		content := strings.Replace(htmlContent, "<!--SERVER_CONFIG-->", configScript, 1)
		fmt.Fprint(w, content)
	})
}

func main() {
	logFile, err := os.OpenFile("server.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Println("Failed to open log file:", err)
	} else {
		defer logFile.Close()
		multi := io.MultiWriter(os.Stdout, logFile)
		log.SetOutput(multi)
	}
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	log.Println("=== Starting Maze Runner Server Session ===")
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("+------------------------------------------+")
	fmt.Println("|         MAZE RUNNER SERVER                |")
	fmt.Println("+------------------------------------------+")
	fmt.Println("|  Select start mode:                      |")
	fmt.Println("|                                          |")
	fmt.Println("|  [1] Game server only (WebSocket API)    |")
	fmt.Println("|  [2] Website only (static page)          |")
	fmt.Println("|  [3] Both (server + website)             |")
	fmt.Println("|                                          |")
	fmt.Println("+------------------------------------------+")
	fmt.Print("\nYour choice (1/2/3): ")
	choice := readLine(reader)

	switch choice {
	case "1":
		log.Println("Mode 1: Starting Game Server only")
	case "2":
		log.Println("Mode 2: Starting Website only")
	case "3":
		log.Println("Mode 3: Starting Server + Website")
	default:
		choice = "3"
		log.Println("Invalid choice, defaulting to Mode 3")
	}

	// Only ask for maze size if we are running a game server (Mode 1 or 3)
	if choice != "2" {
		fmt.Println("\n+------------------------------------------+")
		fmt.Println("|  Maze size:                              |")
		fmt.Println("|                                          |")
		fmt.Println("|  [1] Small  (31x21)                      |")
		fmt.Println("|  [2] Medium (71x41)  [default]           |")
		fmt.Println("|  [3] Large  (101x61)                     |")
		fmt.Println("|  [4] Huge   (151x81)                     |")
		fmt.Println("|  [5] Custom                              |")
		fmt.Println("|                                          |")
		fmt.Println("+------------------------------------------+")
		fmt.Print("\nYour choice (1-5): ")
		sizeChoice := readLine(reader)

		switch sizeChoice {
		case "1":
			mazeWidth, mazeHeight = 31, 21
		case "3":
			mazeWidth, mazeHeight = 101, 61
		case "4":
			mazeWidth, mazeHeight = 151, 81
		case "5":
			fmt.Print("Width (odd number): ")
			wStr := readLine(reader)
			fmt.Print("Height (odd number): ")
			hStr := readLine(reader)
			w, _ := strconv.Atoi(wStr)
			h, _ := strconv.Atoi(hStr)
			if w < 11 || h < 11 {
				mazeWidth, mazeHeight = 71, 41
			} else {
				if w%2==0 { w++ }
				if h%2==0 { h++ }
				mazeWidth, mazeHeight = w, h
			}
		default:
			mazeWidth, mazeHeight = 71, 41
		}
		log.Printf("Selected maze size: %dx%d", mazeWidth, mazeHeight)
	}

	// --- Port Configuration ---
	fmt.Println("\n+------------------------------------------+")
	fmt.Println("|  Port Configuration                      |")
	fmt.Println("+------------------------------------------+")
	
	var gamePort, webPort string

	if choice == "1" {
		fmt.Print("Game Server Port [8080]: ")
		gamePort = readLine(reader)
		if gamePort == "" { gamePort = "8080" }
	} else if choice == "2" {
		fmt.Print("Website Port [8080]: ")
		webPort = readLine(reader)
		if webPort == "" { webPort = "8080" }
	} else {
		// Mode 3
		fmt.Print("Website Port [8080]: ")
		webPort = readLine(reader)
		if webPort == "" { webPort = "8080" }
		
		fmt.Printf("Game Server Port [%s]: ", webPort)
		gamePort = readLine(reader)
		if gamePort == "" { gamePort = webPort }
	}

	log.Printf("Ports configured - Web: %s, Game: %s", webPort, gamePort)

	if choice != "2" {
		generateMaze()
	}
	startTime = time.Now()

	var wg sync.WaitGroup

	// --- Start Servers ---
	if choice == "1" {
		// Game Only
		mux := http.NewServeMux()
		setupGameHandlers(mux)
		log.Printf("Starting Game Server on port %s...", gamePort)
		if err := http.ListenAndServe(":"+gamePort, mux); err != nil {
			log.Fatalf("Game Server failed: %v", err)
		}
	} else if choice == "2" {
		// Website Only
		mux := http.NewServeMux()
		// No game port known/needed really, user must input manual IP if game server exists elsewhere
		setupWebsiteHandlers(mux, "") 
		log.Printf("Starting Website on port %s...", webPort)
		if err := http.ListenAndServe(":"+webPort, mux); err != nil {
			log.Fatalf("Website failed: %v", err)
		}
	} else {
		// Both
		if gamePort == webPort {
			// Single Server
			mux := http.NewServeMux()
			setupGameHandlers(mux)
			setupWebsiteHandlers(mux, gamePort)
			log.Printf("Starting Combined Server on port %s...", webPort)
			if err := http.ListenAndServe(":"+webPort, mux); err != nil {
				log.Fatalf("Server failed: %v", err)
			}
		} else {
			// Dual Server
			wg.Add(2)
			
			go func() {
				defer wg.Done()
				mux := http.NewServeMux()
				setupGameHandlers(mux)
				log.Printf("Starting Game Server on port %s...", gamePort)
				if err := http.ListenAndServe(":"+gamePort, mux); err != nil {
					log.Println("Game Server failed:", err)
				}
			}()

			go func() {
				defer wg.Done()
				mux := http.NewServeMux()
				setupWebsiteHandlers(mux, gamePort)
				log.Printf("Starting Website on port %s...", webPort)
				if err := http.ListenAndServe(":"+webPort, mux); err != nil {
					log.Println("Website failed:", err)
				}
			}()

			wg.Wait()
		}
	}
}

const htmlContent = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Maze Runner</title>
<!--SERVER_CONFIG-->
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{background:#111;color:#ccc;font-family:system-ui,sans-serif;display:flex;flex-direction:column;align-items:center;justify-content:center;height:100vh;overflow:hidden}
#ui{background:#1a1a1a;padding:32px 40px;border-radius:16px;text-align:center;border:1px solid #2a2a2a;max-width:420px;width:90vw}
#ui h1{font-size:2.2rem;font-weight:800;color:#e8e8e8;margin-bottom:4px}
#ui .sub{color:#666;font-size:.8rem;margin-bottom:24px;letter-spacing:2px}
.fg{margin-bottom:16px;text-align:left}
.fg label{display:block;font-size:.7rem;letter-spacing:1px;color:#555;margin-bottom:6px;text-transform:uppercase}
.fg input[type=text]{width:100%;padding:10px 14px;background:#222;border:1px solid #333;border-radius:8px;color:#eee;font-size:.95rem;outline:none}
.fg input[type=text]:focus{border-color:#555}
.srv{margin-bottom:16px;padding:12px;background:#161616;border:1px dashed #2a2a2a;border-radius:8px}
.srv .hint{font-size:.65rem;color:#444;margin-top:4px}
.colors{display:grid;grid-template-columns:repeat(8,1fr);gap:6px;margin-bottom:10px}
.cs{aspect-ratio:1;border-radius:6px;cursor:pointer;border:2px solid transparent;transition:border-color .15s}
.cs:hover{border-color:rgba(255,255,255,.3)}
.cs.sel{border-color:#fff}
.ccr{display:flex;align-items:center;gap:8px;margin-bottom:20px}
.ccr input[type=color]{width:32px;height:32px;border:none;border-radius:6px;cursor:pointer;background:none;padding:0}
.ccr input[type=color]::-webkit-color-swatch-wrapper{padding:1px}
.ccr input[type=color]::-webkit-color-swatch{border-radius:5px;border:none}
.ccr span{font-size:.75rem;color:#555}
.cprev{width:32px;height:32px;border-radius:6px;border:1px solid #333}
#startBtn{width:100%;padding:14px;font-size:1rem;font-weight:700;border:none;border-radius:10px;cursor:pointer;background:#e8e8e8;color:#111;transition:background .2s}
#startBtn:hover{background:#fff}
#langBtn{position:absolute;top:12px;right:12px;background:none;border:1px solid #333;color:#888;padding:4px 10px;border-radius:6px;cursor:pointer;font-size:.75rem}
#langBtn:hover{border-color:#555;color:#ccc}
canvas{display:none;border-radius:8px}
#lb{position:fixed;top:16px;right:16px;background:rgba(17,17,17,.92);padding:14px 16px;border-radius:10px;border:1px solid #222;min-width:180px;display:none;font-size:.8rem}
#lb h3{font-size:.65rem;letter-spacing:2px;color:#555;margin-bottom:8px;text-transform:uppercase}
.le{display:flex;align-items:center;padding:4px 0;gap:6px;border-bottom:1px solid #1a1a1a}
.le .rk{width:20px;height:20px;border-radius:4px;display:flex;align-items:center;justify-content:center;font-size:.65rem;font-weight:700;background:#222}
.le .rk.g{background:#b8860b;color:#fff}.le .rk.s{background:#708090;color:#fff}.le .rk.br{background:#8B4513;color:#fff}
.ld{width:6px;height:6px;border-radius:50%;flex-shrink:0}
.fb{font-size:.55rem;background:#2d5a2d;padding:1px 5px;border-radius:3px;color:#8f8;margin-left:auto}
#tm{position:fixed;top:16px;left:16px;background:rgba(17,17,17,.92);padding:10px 14px;border-radius:10px;border:1px solid #222;display:none;font-size:1.3rem;font-weight:700;color:#888}
#tm .tl{font-size:.55rem;letter-spacing:1px;color:#444;display:block}
#pc{position:fixed;bottom:16px;left:16px;font-size:.7rem;color:#444;display:none}
#go{display:none;position:fixed;inset:0;background:rgba(0,0,0,.92);z-index:1000;flex-direction:column;align-items:center;justify-content:center}
.goc{background:#1a1a1a;padding:40px;border-radius:16px;text-align:center;max-width:440px;width:90vw;border:1px solid #2a2a2a}
.goc h2{font-size:2rem;font-weight:800;color:#e8e8e8;margin-bottom:4px}
.goc .gs{color:#666;font-size:.8rem;margin-bottom:24px}
.fr{text-align:left;margin-bottom:24px}
.fre{display:flex;align-items:center;padding:10px 12px;margin-bottom:6px;background:#161616;border-radius:8px;gap:10px}
.fre:first-child{background:#1a1700;border:1px solid #333000}
.frn{font-size:1.2rem;font-weight:800;width:32px;color:#444}
.fre:nth-child(1) .frn{color:#b8860b}
.fre:nth-child(2) .frn{color:#708090}
.fre:nth-child(3) .frn{color:#8B4513}
.frc{width:10px;height:10px;border-radius:50%}
.frname{flex:1;font-weight:600;font-size:.9rem}
.frt{font-size:.8rem;color:#555;font-family:monospace}
#bb{padding:12px 32px;font-size:.9rem;font-weight:600;border:1px solid #333;border-radius:10px;cursor:pointer;background:transparent;color:#ccc;transition:background .2s}
#bb:hover{background:#222}
#mc{display:none;position:fixed;bottom:16px;right:16px;z-index:100}
.dp{display:grid;grid-template-columns:44px 44px 44px;grid-template-rows:44px 44px 44px;gap:3px}
.dp button{background:#1a1a1a;border:1px solid #2a2a2a;border-radius:8px;color:#888;font-size:1rem;cursor:pointer}
.dp button:active{background:#333}
.dp .em{background:none;border:none}
@media(hover:none)and(pointer:coarse){#mc{display:block!important}}
</style>
</head>
<body>
<div id="lb"></div>
<div id="tm"><span class="tl" data-i="time">Time</span><span id="tv">00:00</span></div>
<div id="pc"></div>
<div id="ui" style="position:relative">
    <button id="langBtn" onclick="toggleLang()">DE</button>
    <h1>MAZE RUNNER</h1>
    <p class="sub">MULTIPLAYER LABYRINTH</p>
    <div class="fg"><label data-i="playerName">Player Name</label><input type="text" id="name" data-pi="namePh" placeholder="Enter name..." maxlength="12"></div>
    <div class="srv"><div class="fg" style="margin:0"><label data-i="serverIp">Server IP (optional)</label><input type="text" id="sip" placeholder="e.g. 192.168.1.100:8080"></div><p class="hint" data-i="serverHint">Leave empty = current server</p></div>
    <label style="font-size:.65rem;letter-spacing:1px;color:#555;text-transform:uppercase" data-i="color">Color</label>
    <div class="colors" id="co" style="margin-top:6px"></div>
    <div class="ccr"><input type="color" id="cc" value="#4a9eff"><span data-i="customColor">custom color</span><div style="flex:1"></div><div class="cprev" id="cp" style="background:#4a9eff"></div></div>
    <button id="startBtn" onclick="start()" data-i="startGame">START GAME</button>
</div>
<canvas id="c"></canvas>
<div id="mc"><div class="dp">
    <div class="em"></div><button onclick="move(0,-1)">&#9650;</button><div class="em"></div>
    <button onclick="move(-1,0)">&#9668;</button><div class="em"></div><button onclick="move(1,0)">&#9658;</button>
    <div class="em"></div><button onclick="move(0,1)">&#9660;</button><div class="em"></div>
</div></div>
<div id="go"><div class="goc">
    <h2 data-i="gameOver">GAME OVER</h2>
    <p class="gs" data-i="allFinished">All players reached the goal!</p>
    <div class="fr" id="frs"></div>
    <button id="bb" onclick="backToMenu()" data-i="backMenu">Back to Menu</button>
</div></div>

<script>
const canvas=document.getElementById('c'),ctx=canvas.getContext('2d');
let maze=[],ws,myPlayer={x:1,y:1,name:"",color:"#4a9eff",finished:false};
let gameStartTime=0,timerInterval=null,selColor="#4a9eff",gameEnded=false;
let mazeCanvas=null,camX=0,camY=0,lastPlayers=[];
let GOALX=69,GOALY=39,MW=71,MH=41;
const CELL=14,VIEWW=800,VIEWH=560;

// --- i18n ---
let lang='en';
const T={
    en:{playerName:"Player Name",namePh:"Enter name...",serverIp:"Server IP (optional)",serverHint:"Leave empty = current server",color:"Color",customColor:"custom color",startGame:"START GAME",time:"Time",ranking:"Ranking",goal:"GOAL",players:"Players",atGoal:"at goal",gameOver:"GAME OVER",allFinished:"All players reached the goal!",backMenu:"Back to Menu",connFail:"Connection failed!",error:"Error"},
    de:{playerName:"Spielername",namePh:"Name eingeben...",serverIp:"Server IP (optional)",serverHint:"Leer lassen = aktueller Server",color:"Farbe",customColor:"eigene Farbe",startGame:"SPIEL STARTEN",time:"Zeit",ranking:"Rangliste",goal:"ZIEL",players:"Spieler",atGoal:"am Ziel",gameOver:"SPIEL VORBEI",allFinished:"Alle Spieler haben das Ziel erreicht!",backMenu:"Zurueck zum Menue",connFail:"Verbindung fehlgeschlagen!",error:"Fehler"}
};
function t(k){return T[lang][k]||k}
function applyLang(){
    document.querySelectorAll('[data-i]').forEach(el=>{el.textContent=t(el.dataset.i)});
    document.querySelectorAll('[data-pi]').forEach(el=>{el.placeholder=t(el.dataset.pi)});
    document.getElementById('langBtn').textContent=lang==='en'?'DE':'EN';
}
function toggleLang(){lang=lang==='en'?'de':'en';applyLang()}
applyLang();

const colors=["#e74c3c","#e67e22","#f1c40f","#2ecc71","#1abc9c","#3498db","#4a9eff","#9b59b6","#e84393","#fd79a8","#00cec9","#6c5ce7","#a29bfe","#ffeaa7","#dfe6e9","#636e72"];

function renderColors(){
    const c=document.getElementById('co');c.innerHTML='';
    colors.forEach(cl=>{const d=document.createElement('div');d.className='cs'+(cl===selColor?' sel':'');d.style.background=cl;d.onclick=()=>{selColor=cl;document.getElementById('cp').style.background=cl;document.getElementById('cc').value=cl;renderColors()};c.appendChild(d)})
}
document.getElementById('cc').addEventListener('input',e=>{selColor=e.target.value;document.getElementById('cp').style.background=e.target.value;renderColors()});
renderColors();

function startTimer(){
    gameStartTime=Date.now();
    timerInterval=setInterval(()=>{if(gameEnded)return;const s=Math.floor((Date.now()-gameStartTime)/1000);document.getElementById('tv').textContent=String(Math.floor(s/60)).padStart(2,'0')+':'+String(s%60).padStart(2,'0')},1000)
}

function move(dx,dy){
    if(myPlayer.finished||gameEnded)return;
    let nx=myPlayer.x+dx,ny=myPlayer.y+dy;
    if(maze[ny]&&maze[ny][nx]===0){myPlayer.x=nx;myPlayer.y=ny;if(nx===GOALX&&ny===GOALY)myPlayer.finished=true;send()}
}

function buildMazeCanvas(){
    mazeCanvas=document.createElement('canvas');
    mazeCanvas.width=maze[0].length*CELL;
    mazeCanvas.height=maze.length*CELL;
    const mc=mazeCanvas.getContext('2d');
    for(let y=0;y<maze.length;y++){
        for(let x=0;x<maze[y].length;x++){
            if(maze[y][x]===0){
                mc.fillStyle=(x+y)%2===0?'#1a1a1a':'#1c1c1c';
                mc.fillRect(x*CELL,y*CELL,CELL,CELL);
            }
        }
    }
    for(let y=0;y<maze.length;y++){
        for(let x=0;x<maze[y].length;x++){
            if(maze[y][x]===1){
                const px=x*CELL,py=y*CELL;
                mc.fillStyle='#2a2a2e';mc.fillRect(px,py,CELL,CELL);
                mc.fillStyle='#353540';mc.fillRect(px,py,CELL,2);
                mc.fillStyle='#30303a';mc.fillRect(px,py,2,CELL);
                mc.fillStyle='#1a1a20';mc.fillRect(px,py+CELL-1,CELL,1);mc.fillRect(px+CELL-1,py,1,CELL);
                if((x*7+y*13)%5===0){mc.fillStyle='rgba(255,255,255,0.03)';mc.fillRect(px+4,py+4,2,2)}
            }
        }
    }
    const gx=GOALX*CELL,gy=GOALY*CELL;
    mc.fillStyle='#2a2200';mc.fillRect(gx-CELL,gy-CELL,CELL*3,CELL*3);
    mc.fillStyle='#3a3200';mc.fillRect(gx,gy,CELL,CELL);
}

async function start(){
    myPlayer.name=document.getElementById('name').value||"Runner";
    myPlayer.color=selColor;myPlayer.x=1;myPlayer.y=1;myPlayer.finished=false;gameEnded=false;
    const sip=document.getElementById('sip').value.trim();
    
    // Auto-detect host logic
    let host=sip;
    if(!host) {
        // Use default game port injected by server if available, otherwise window.location.host
        if(window.DEFAULT_GAME_PORT) {
             host = window.location.hostname + ":" + window.DEFAULT_GAME_PORT;
        } else {
             host = window.location.host;
        }
    }
    // Fallback if port missing but needed? usually location.host includes port.
    // If user enters IP without port, adding default 8080 isn't always right if game runs on different port.
    // But for simplicty:
    if(host && !host.includes(':') && !window.DEFAULT_GAME_PORT) host=host+':8080';
    
    const pr=location.protocol==='https:'?'https':'http';
    const wpr=location.protocol==='https:'?'wss':'ws';
    try{
        const infoRes=await fetch(pr+'://'+host+'/info');
        const info=await infoRes.json();
        GOALX=info.goalX;GOALY=info.goalY;MW=info.width;MH=info.height;

        const res=await fetch(pr+'://'+host+'/maze');maze=await res.json();
        canvas.width=VIEWW;canvas.height=VIEWH;
        buildMazeCanvas();
        ws=new WebSocket(wpr+'://'+host+'/ws');
        ws.onopen=()=>{
            document.getElementById('ui').style.display='none';
            canvas.style.display='block';
            document.getElementById('lb').style.display='block';
            document.getElementById('tm').style.display='block';
            document.getElementById('pc').style.display='block';
            startTimer();send();requestAnimationFrame(gameLoop);
        };
        ws.onmessage=e=>{
            const st=JSON.parse(e.data);lastPlayers=st.players||[];
            if(st.allFinished&&st.players&&st.players.length>0&&!gameEnded){gameEnded=true;clearInterval(timerInterval);showGameOver(st.players)}
        };
        ws.onerror=()=>alert(t('connFail'));
        ws.onclose=()=>{if(!gameEnded)console.log("Disconnected")};
        window.onkeydown=e=>{
            if(myPlayer.finished||gameEnded)return;
            let dx=0,dy=0;
            if(e.key==="ArrowUp"||e.key==="w")dy=-1;
            if(e.key==="ArrowDown"||e.key==="s")dy=1;
            if(e.key==="ArrowLeft"||e.key==="a")dx=-1;
            if(e.key==="ArrowRight"||e.key==="d")dx=1;
            if(dx||dy){e.preventDefault();move(dx,dy)}
        };
    }catch(err){alert(t('error')+': '+err)}
}

function gameLoop(){
    if(gameEnded)return;
    draw(lastPlayers);
    requestAnimationFrame(gameLoop);
}

function draw(players){
    const targetCX=myPlayer.x*CELL-VIEWW/2+CELL/2;
    const targetCY=myPlayer.y*CELL-VIEWH/2+CELL/2;
    camX+=(targetCX-camX)*0.12;camY+=(targetCY-camY)*0.12;
    const mw=maze[0].length*CELL,mh=maze.length*CELL;
    camX=Math.max(0,Math.min(camX,mw-VIEWW));
    camY=Math.max(0,Math.min(camY,mh-VIEWH));

    ctx.fillStyle="#111";ctx.fillRect(0,0,VIEWW,VIEWH);
    ctx.drawImage(mazeCanvas,-camX,-camY);

    const gx=GOALX*CELL-camX,gy=GOALY*CELL-camY;
    const tt=Date.now()/1000;
    ctx.fillStyle='#888';ctx.fillRect(gx+2,gy-8,2,CELL+8);
    const wave=Math.sin(tt*3)*2;
    ctx.fillStyle='#d4aa00';ctx.beginPath();ctx.moveTo(gx+4,gy-8);ctx.lineTo(gx+14+wave,gy-4);ctx.lineTo(gx+4,gy);ctx.fill();

    const sorted=[...players].sort((a,b)=>{
        if(a.finished&&!b.finished)return -1;if(!a.finished&&b.finished)return 1;
        if(a.finished&&b.finished)return a.finishRank-b.finishRank;return 0
    });

    let totalP=players.length,finP=players.filter(p=>p.finished).length;
    document.getElementById('pc').textContent=totalP+' '+t('players')+' | '+finP+' '+t('atGoal');

    let lh='<h3>'+t('ranking')+'</h3>';
    sorted.forEach(p=>{
        const rc=p.finished?(p.finishRank===1?'g':p.finishRank===2?'s':p.finishRank===3?'br':''):'';
        lh+='<div class="le"><div class="rk '+rc+'">'+(p.finished?p.finishRank:'·')+'</div>';
        lh+='<div class="ld" style="background:'+p.color+'"></div><span>'+p.name+'</span>';
        if(p.finished)lh+='<span class="fb">'+t('goal')+'</span>';
        lh+='</div>';
    });
    document.getElementById('lb').innerHTML=lh;

    sorted.forEach(p=>{
        if(p.finished)return;
        const px=p.x*CELL-camX,py=p.y*CELL-camY;
        if(px<-CELL||px>VIEWW+CELL||py<-CELL||py>VIEWH+CELL)return;
        ctx.fillStyle='rgba(0,0,0,0.4)';ctx.beginPath();ctx.ellipse(px+CELL/2,py+CELL-1,CELL/2-1,3,0,0,Math.PI*2);ctx.fill();
        ctx.fillStyle=p.color;ctx.beginPath();ctx.arc(px+CELL/2,py+CELL/2,CELL/2-1,0,Math.PI*2);ctx.fill();
        ctx.fillStyle='rgba(255,255,255,0.2)';ctx.beginPath();ctx.arc(px+CELL/2-1,py+CELL/2-2,CELL/4,0,Math.PI*2);ctx.fill();
        ctx.font='bold 9px system-ui';
        const tw=ctx.measureText(p.name).width;
        ctx.fillStyle='rgba(0,0,0,0.6)';
        const tagX=px+CELL/2-tw/2-3,tagY=py-12;
        ctx.fillRect(tagX,tagY,tw+6,12);
        ctx.fillStyle='#eee';ctx.fillText(p.name,tagX+3,tagY+9);
    });
}

function send(){if(ws&&ws.readyState===1)ws.send(JSON.stringify(myPlayer))}

function showGameOver(players){
    document.getElementById('go').style.display='flex';canvas.style.display='none';
    const r=document.getElementById('frs');
    const s=[...players].sort((a,b)=>a.finishRank-b.finishRank);
    let h='';
    s.forEach((p,i)=>{
        const m=(i+1)+'.';
        const ts=p.finishTime?Math.floor(p.finishTime/60)+':'+String(p.finishTime%60).padStart(2,'0'):'--';
        h+='<div class="fre"><div class="frn">'+m+'</div><div class="frc" style="background:'+p.color+'"></div><div class="frname">'+p.name+'</div><div class="frt">'+ts+'</div></div>';
    });
    r.innerHTML=h;
    applyLang();
}

function backToMenu(){
    if(ws)ws.close();clearInterval(timerInterval);
    document.getElementById('go').style.display='none';canvas.style.display='none';
    document.getElementById('lb').style.display='none';document.getElementById('tm').style.display='none';
    document.getElementById('pc').style.display='none';document.getElementById('ui').style.display='block';
    myPlayer={x:1,y:1,name:myPlayer.name,color:myPlayer.color,finished:false};gameEnded=false;
}
</script>
</body>
</html>
`

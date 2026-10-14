// Monitor  -> telemetria sintetica
// Analyze  -> Neumotron (Ollama, nemotron-3-nano:4b), salida JSON forzada por esquema
// Plan     -> Arbiter (motor de reglas + salvaguardas)
// Execute  -> microservicio Go (Goroutines concurrentes)
// Knowledge -> historial persistido en knowledge_log.json
package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

//go:embed static/index.html
var staticFiles embed.FS

const (
	ollamaURL   = "http://localhost:11434/api/chat"
	modelName   = "nemotron-3-nano:4b"
	knowledgeDB = "knowledge_log.json"

	rateLimitWindow    = 5
	rateLimitMaxAccion = 2
	umbralConfianza    = 0.7
)

var (
	nodos        = []string{"web-01", "web-02", "db-01"}
	ollamaClient = &http.Client{Timeout: 60 * time.Second}
	knowledgeMu  sync.RWMutex
)

// ---------- Fase 1: Monitor ----------

type Telemetria struct {
	Nodo string
	CPU  float64
	RAM  float64
}

func generarTelemetria(forzarAnomaliaEn string) []Telemetria {
	lecturas := make([]Telemetria, 0, len(nodos))
	for _, nodo := range nodos {
		cpu := 20 + rand.Float64()*35
		ram := 30 + rand.Float64()*30
		if nodo == forzarAnomaliaEn {
			cpu = 88 + rand.Float64()*11
		}
		lecturas = append(lecturas, Telemetria{Nodo: nodo, CPU: cpu, RAM: ram})
	}
	return lecturas
}

// ---------- Fase 3: Analyze (Neumotron) ----------

type Analisis struct {
	Anomaly  bool    `json:"anomaly"`
	Type     string  `json:"type"`
	Severity float64 `json:"severity"`
}

const systemPromptNeumotron = "Eres Neumotron, el componente Analyze de una arquitectura MAPE-K " +
	"para monitoreo de infraestructura de servidores. Recibes una lectura de telemetria " +
	"y debes evaluar si representa una anomalia, clasificarla y estimar su severidad."

const schemaAnalisis = `{
  "type": "object",
  "properties": {
    "anomaly": {"type": "boolean"},
    "type": {"type": "string", "enum": ["cpu_spike", "memory_leak", "network_saturation", "none"]},
    "severity": {"type": "number"}
  },
  "required": ["anomaly", "type", "severity"]
}`

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string                 `json:"model"`
	Messages []chatMessage          `json:"messages"`
	Stream   bool                   `json:"stream"`
	Think    bool                   `json:"think"`
	Format   json.RawMessage        `json:"format,omitempty"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

type chatResponse struct {
	Message chatMessage `json:"message"`
	Done    bool        `json:"done"`
}

func analizarConNeumotron(lectura Telemetria) (Analisis, error) {
	prompt := fmt.Sprintf(
		"Lectura de telemetria del nodo %s: CPU=%.1f%%, RAM=%.1f%%. Evalua si representa una anomalia.",
		lectura.Nodo, lectura.CPU, lectura.RAM,
	)

	reqBody := chatRequest{
		Model:  modelName,
		Stream: false,
		Think:  false,
		Format: json.RawMessage(schemaAnalisis),
		Messages: []chatMessage{
			{Role: "system", Content: systemPromptNeumotron},
			{Role: "user", Content: prompt},
		},
		Options: map[string]interface{}{"temperature": 0.2},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return Analisis{}, err
	}

	resp, err := ollamaClient.Post(ollamaURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return Analisis{}, fmt.Errorf("no se pudo contactar a Ollama en %s: %w", ollamaURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return Analisis{}, fmt.Errorf("ollama HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(respBody))
	}

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return Analisis{}, err
	}

	var analisis Analisis
	if err := json.Unmarshal([]byte(cr.Message.Content), &analisis); err != nil {
		return Analisis{}, fmt.Errorf("respuesta de Neumotron no valida: %s", cr.Message.Content)
	}
	return analisis, nil
}

func analizarMock(lectura Telemetria) (Analisis, error) {
	if lectura.CPU > 85 {
		return Analisis{Anomaly: true, Type: "cpu_spike", Severity: 0.9}, nil
	}
	return Analisis{Anomaly: false, Type: "none", Severity: 0.1}, nil
}

// ---------- Fase Plan: Arbiter ----------

type Decision struct {
	Ciclo  int    `json:"ciclo"`
	Accion string `json:"accion"`
	Razon  string `json:"razon"`
}

type Historial map[string][]Decision

func arbiterDecidir(nodo string, ciclo int, analisis Analisis, historial Historial) Decision {
	if !analisis.Anomaly || analisis.Severity < umbralConfianza {
		return Decision{Ciclo: ciclo, Accion: "sin_accion", Razon: "confianza insuficiente o sin anomalia"}
	}

	eventos := historial[nodo]
	inicio := 0
	if len(eventos) > rateLimitWindow {
		inicio = len(eventos) - rateLimitWindow
	}
	accionesRecientes := 0
	for _, e := range eventos[inicio:] {
		if e.Accion != "sin_accion" && e.Accion != "escalar_a_humano" {
			accionesRecientes++
		}
	}
	if accionesRecientes >= rateLimitMaxAccion {
		return Decision{Ciclo: ciclo, Accion: "escalar_a_humano", Razon: "limite de frecuencia excedido"}
	}

	var accion string
	switch analisis.Type {
	case "cpu_spike":
		accion = "escalamiento_horizontal"
	case "memory_leak":
		accion = "reinicio_de_servicio"
	case "network_saturation":
		accion = "rebalanceo_de_trafico"
	default:
		accion = "notificar"
	}
	return Decision{Ciclo: ciclo, Accion: accion, Razon: fmt.Sprintf("regla aplicada para %s", analisis.Type)}
}

// ---------- Knowledge: persistencia del historial ----------

func cargarHistorial() Historial {
	knowledgeMu.RLock()
	defer knowledgeMu.RUnlock()

	datos, err := os.ReadFile(knowledgeDB)
	if err != nil {
		return Historial{}
	}
	var h Historial
	if err := json.Unmarshal(datos, &h); err != nil {
		return Historial{}
	}
	return h
}

func guardarHistorial(nuevasAcciones Historial) error {
	knowledgeMu.Lock()
	defer knowledgeMu.Unlock()

	actual := Historial{}
	if datos, err := os.ReadFile(knowledgeDB); err == nil {
		_ = json.Unmarshal(datos, &actual)
	}

	for nodo, decisiones := range nuevasAcciones {
		actual[nodo] = append(actual[nodo], decisiones...)
	}

	datos, err := json.MarshalIndent(actual, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(knowledgeDB, datos, 0644)
}

// ---------- Servidor web ----------

func indexHandler(w http.ResponseWriter, r *http.Request) {
	data, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "no se encontro static/index.html", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func knowledgeHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cargarHistorial())
}

func streamHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming no soportado", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	q := r.URL.Query()
	ciclos := 6
	if v := q.Get("ciclos"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			ciclos = n
		}
	}
	mock := q.Get("mock") == "true"

	var writeMu sync.Mutex
	send := func(event string, data interface{}) {
		writeMu.Lock()
		defer writeMu.Unlock()
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}

	runHistorial := make(Historial)

	for ciclo := 1; ciclo <= ciclos; ciclo++ {
		if r.Context().Err() != nil {
			return
		}

		forzar := ""
		if ciclo >= 3 && ciclo <= 5 {
			forzar = nodos[0]
		}
		lecturas := generarTelemetria(forzar)
		var wg sync.WaitGroup

		for _, lectura := range lecturas {
			if r.Context().Err() != nil {
				return
			}

			send("monitor", map[string]interface{}{
				"ciclo": ciclo, "nodo": lectura.Nodo, "cpu": lectura.CPU, "ram": lectura.RAM,
			})

			var analisis Analisis
			var err error
			if mock {
				analisis, err = analizarMock(lectura)
			} else {
				analisis, err = analizarConNeumotron(lectura)
			}
			if err != nil {
				send("error", map[string]string{"nodo": lectura.Nodo, "mensaje": err.Error()})
				continue
			}
			send("analyze", map[string]interface{}{
				"ciclo": ciclo, "nodo": lectura.Nodo, "anomaly": analisis.Anomaly,
				"type": analisis.Type, "severity": analisis.Severity,
			})

			decision := arbiterDecidir(lectura.Nodo, ciclo, analisis, runHistorial)
			send("plan", map[string]interface{}{
				"ciclo": ciclo, "nodo": lectura.Nodo, "accion": decision.Accion, "razon": decision.Razon,
			})

			if decision.Accion != "sin_accion" {
				runHistorial[lectura.Nodo] = append(runHistorial[lectura.Nodo], decision)

				wg.Add(1)
				go func(nodo string, dec Decision) {
					defer wg.Done()
					time.Sleep(150 * time.Millisecond)
					send("execute", map[string]interface{}{
						"ciclo": ciclo, "nodo": nodo, "accion": dec.Accion,
					})
				}(lectura.Nodo, decision)
			}
		}

		wg.Wait()
		send("cycle_end", map[string]int{"ciclo": ciclo})
		time.Sleep(300 * time.Millisecond)
	}

	if err := guardarHistorial(runHistorial); err != nil {
		send("error", map[string]string{"mensaje": "no se pudo guardar knowledge_log.json: " + err.Error()})
	}
	send("done", map[string]bool{"ok": true})
}

func main() {
	port := flag.String("port", "8090", "puerto del servidor web")
	flag.Parse()

	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/stream", streamHandler)
	http.HandleFunc("/knowledge", knowledgeHandler)

	addr := ":" + *port
	fmt.Printf("=== SmartOps demo web ===\n")
	fmt.Printf("Abre http://localhost%s en tu navegador\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/slack-go/slack"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

var (
	state          bool
	optInUsers     = make(map[string]bool)
	optInUsersLock sync.RWMutex
)

// Constants for configuration
const (
	slackVerificationToken = "your-slack-verification-token"
	logDir                 = "logs"
	logFileName            = "app.log"
	logCleanupInterval     = time.Hour
	logRetentionDuration   = 24 * time.Hour
	pollingInterval        = 100 * time.Millisecond
)

func main() {
	slackToken := getEnv("SLACK_TOKEN")
	slackChannel := getEnv("SLACK_CHANNEL")

	initializeGPIO()
	defer startHTTPServer()

	logFile := setupLogging()
	defer logFile.Close()

	pin := setupGPIOPin("GPIO17")
	go monitorSwitch(pin, slackToken, slackChannel)
}

// getEnv retrieves environment variables and exits on missing variables.
func getEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Fatalf("Environment variable %s must be set", key)
	}
	return value
}

// initializeGPIO initializes the GPIO library.
func initializeGPIO() {
	if _, err := host.Init(); err != nil {
		log.Fatalf("Failed to initialize GPIO: %v", err)
	}
}

// setupGPIOPin configures a GPIO pin as input with a pull-up resistor.
func setupGPIOPin(pinName string) gpio.PinIO {
	pin := gpioreg.ByName(pinName)
	if pin == nil {
		log.Fatalf("Failed to find pin %s", pinName)
	}
	if err := pin.In(gpio.PullUp, gpio.BothEdges); err != nil {
		log.Fatalf("Failed to configure pin %s as input: %v", pinName, err)
	}
	return pin
}

// setupLogging sets up logging to a file with rotation for old logs.
func setupLogging() *os.File {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Fatalf("Failed to create log directory: %v", err)
	}

	logFile, err := os.OpenFile(fmt.Sprintf("%s/%s", logDir, logFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open log file: %v", err)
	}

	log.SetOutput(logFile)
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	go cleanupOldLogs()

	return logFile
}

// cleanupOldLogs deletes log entries older than the retention duration.
func cleanupOldLogs() {
	for {
		time.Sleep(logCleanupInterval)

		logEntries, err := os.ReadFile(fmt.Sprintf("%s/%s", logDir, logFileName))
		if err != nil {
			log.Printf("Failed to read log file: %v", err)
			continue
		}

		var recentLogs []byte
		cutoff := time.Now().Add(-logRetentionDuration)

		for _, entry := range bytes.Split(logEntries, []byte("\n")) {
			if len(entry) == 0 {
				continue
			}
			if logTime, err := time.Parse(time.RFC3339, string(entry[:20])); err == nil && logTime.After(cutoff) {
				recentLogs = append(recentLogs, entry...)
				recentLogs = append(recentLogs, '\n')
			}
		}

		if err := os.WriteFile(fmt.Sprintf("%s/%s", logDir, logFileName), recentLogs, 0644); err != nil {
			log.Printf("Failed to write log file: %v", err)
		}
	}
}

// monitorSwitch monitors the GPIO pin and sends Slack messages on state change.
func monitorSwitch(pin gpio.PinIO, slackToken, slackChannel string) {
	var lastState gpio.Level
	for {
		currentState := pin.Read()
		if currentState != lastState {
			lastState = currentState
			state = currentState == gpio.Low
			message := fmt.Sprintf("Switch state changed to: %v", state)
			log.Println(message)
			sendSlackMessage(slackToken, slackChannel, message)
		}
		time.Sleep(pollingInterval)
	}
}

// sendSlackMessage sends a message to the specified Slack channel.
func sendSlackMessage(slackToken, slackChannel, message string) {
	api := slack.New(slackToken)
	_, _, err := api.PostMessage(slackChannel, slack.MsgOptionText(message, false))
	if err != nil {
		log.Printf("Failed to send Slack message: %v", err)
	}
}

// startHTTPServer initializes and starts the HTTP server.
func startHTTPServer() {
	http.HandleFunc("/optin", handleOptIn)
	http.HandleFunc("/status", getStatus)
	log.Println("HTTP server running on port 8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// handleOptIn handles Slack /optin command and updates user preferences.
func handleOptIn(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid request format", http.StatusBadRequest)
		return
	}

	userID := r.FormValue("user_id")
	slackToken := r.FormValue("token")
	if userID == "" || slackToken != slackVerificationToken {
		http.Error(w, "Invalid user or token", http.StatusUnauthorized)
		return
	}

	optInUsersLock.Lock()
	defer optInUsersLock.Unlock()
	optInUsers[userID] = true

	response := map[string]string{
		"response_type": "ephemeral",
		"text":          fmt.Sprintf("You have opted in for notifications, <@%s>.", userID),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// getStatus responds with the current switch state in JSON format.
func getStatus(w http.ResponseWriter, r *http.Request) {
	response := map[string]bool{"state": state}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/fsnotify/fsnotify"
	"github.com/go-redis/redis/v8"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/rs/zerolog"
	"gopkg.in/natefinch/lumberjack.v2"
)

type MessageEnvelope struct {
	Content       json.RawMessage `json:"content"`
	CorrelationID string          `json:"correlation_id,omitempty"`
}

// Global loggers
var (
	appLogger     zerolog.Logger
	messageLogger zerolog.Logger
)

type JavaScriptFunction struct {
	Path    string
	Code    string
	Program *goja.Program
	Topic   string
}

// Store functions with mutex for thread-safe updates
type FunctionStore struct {
	sync.RWMutex
	functions map[string]JavaScriptFunction
}

func NewFunctionStore() *FunctionStore {
	return &FunctionStore{
		functions: make(map[string]JavaScriptFunction),
	}
}

func (fs *FunctionStore) Get(topic string) (JavaScriptFunction, bool) {
	fs.RLock()
	defer fs.RUnlock()
	f, ok := fs.functions[topic]
	return f, ok
}

func (fs *FunctionStore) Set(topic string, function JavaScriptFunction) {
	fs.Lock()
	defer fs.Unlock()
	fs.functions[topic] = function
}

func (fs *FunctionStore) Delete(topic string) {
	fs.Lock()
	defer fs.Unlock()
	delete(fs.functions, topic)
}

// Strip correlation ID and get raw content for JavaScript
func unwrapMessage(payload string) (string, string) {
	var envelope MessageEnvelope
	correlationID := generateCorrelationID() // default if not found

	if err := json.Unmarshal([]byte(payload), &envelope); err == nil {
		if envelope.CorrelationID != "" {
			correlationID = envelope.CorrelationID
			// Remove correlation_id from payload but keep the rest
			var rawMsg map[string]interface{}
			if err := json.Unmarshal([]byte(payload), &rawMsg); err == nil {
				delete(rawMsg, "correlation_id")
				if newPayload, err := json.Marshal(rawMsg); err == nil {
					return string(newPayload), correlationID
				}
			}
		}
	}
	return payload, correlationID // return original if not our format
}

// Wrap content with correlation ID when publishing
func wrapMessage(content string, correlationID string) string {
	// Try to parse the content as JSON first
	var msgMap map[string]interface{}
	if err := json.Unmarshal([]byte(content), &msgMap); err == nil {
		// It's JSON, add correlation_id at the root level
		msgMap["correlation_id"] = correlationID
		if wrapped, err := json.Marshal(msgMap); err == nil {
			return string(wrapped)
		}
	}

	// If it's not JSON or there's an error, wrap it in our envelope
	envelope := MessageEnvelope{
		Content:       json.RawMessage(content),
		CorrelationID: correlationID,
	}
	wrapped, _ := json.Marshal(envelope)
	return string(wrapped)
}

func generateCorrelationID() string {
	return fmt.Sprintf("corr_%d", time.Now().UnixNano())
}

func generateExecutionID() string {
	return fmt.Sprintf("exec_%d", time.Now().UnixNano())
}

func setupLogging() error {
	// Create logs directory if it doesn't exist
	if err := os.MkdirAll("logs", 0755); err != nil {
		return fmt.Errorf("failed to create logs directory: %v", err)
	}

	// Setup application log
	appLogFile := &lumberjack.Logger{
		Filename:   "logs/app.log",
		MaxSize:    10,   // megabytes
		MaxBackups: 3,    // number of backups
		MaxAge:     28,   // days
		Compress:   true, // compress the backups
	}

	// Setup message/execution log
	messageLogFile := &lumberjack.Logger{
		Filename:   "logs/messages.log",
		MaxSize:    10,   // megabytes
		MaxBackups: 3,    // number of backups
		MaxAge:     28,   // days
		Compress:   true, // compress the backups
	}

	// Configure zerolog
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix

	// Create multi-writer for application logs (file + console)
	appMulti := zerolog.MultiLevelWriter(appLogFile, os.Stdout)

	// Set up the loggers
	appLogger = zerolog.New(appMulti).With().Timestamp().Caller().Logger()
	messageLogger = zerolog.New(messageLogFile).With().Timestamp().Logger()

	return nil
}

func loadJavaScriptFunction(path string, root string) (*JavaScriptFunction, error) {
	code, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading file %s: %v", path, err)
	}

	// Get the relative path and remove the "functions/" prefix
	relPath, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("error getting relative path: %v", err)
	}

	prefix := "functions/"
	if strings.HasPrefix(relPath, prefix) {
		relPath = relPath[len(prefix):]
	}

	// Precompile the JavaScript code
	program, err := goja.Compile("", string(code), true)
	if err != nil {
		return nil, fmt.Errorf("error compiling JavaScript for %s: %v", path, err)
	}

	return &JavaScriptFunction{
		Path:    path,
		Code:    string(code),
		Program: program,
		Topic:   relPath,
	}, nil
}

func findJavaScriptFiles(root string) ([]JavaScriptFunction, error) {
	var functions []JavaScriptFunction

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && info.Name() == "func.js" {
			function, err := loadJavaScriptFunction(path, root)
			if err != nil {
				return err
			}
			functions = append(functions, *function)
		}
		return nil
	})

	return functions, err
}

func watchFunctions(ctx context.Context, root string, store *FunctionStore, rdb *redis.Client) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("error creating watcher: %v", err)
	}
	defer watcher.Close()

	// Function to recursively add directories to watcher
	addDirsToWatcher := func(path string) error {
		return filepath.Walk(path, func(walkPath string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if err := watcher.Add(walkPath); err != nil {
					return fmt.Errorf("error adding dir to watcher: %v", err)
				}
				appLogger.Debug().Str("path", walkPath).Msg("Added directory to watcher")
			}
			return nil
		})
	}

	// Initial setup of watched directories
	if err := addDirsToWatcher(root); err != nil {
		return fmt.Errorf("error setting up directory watching: %v", err)
	}

	appLogger.Info().Str("dir", root).Msg("Watching for changes")

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			// Handle directory creation
			if event.Op&fsnotify.Create != 0 {
				fi, err := os.Stat(event.Name)
				if err == nil && fi.IsDir() {
					appLogger.Info().Str("dir", event.Name).Msg("New directory created, adding to watcher")
					if err := addDirsToWatcher(event.Name); err != nil {
						appLogger.Error().Err(err).Str("dir", event.Name).Msg("Failed to watch new directory")
					}
				}
			}

			// Only handle func.js files for function loading
			if filepath.Base(event.Name) != "func.js" {
				continue
			}

			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				appLogger.Info().Str("file", event.Name).Msg("Modified file")
				function, err := loadJavaScriptFunction(event.Name, root)
				if err != nil {
					appLogger.Error().Err(err).Str("file", event.Name).Msg("Error reloading function")
					continue
				}
				store.Set(function.Topic, *function)
				appLogger.Info().Str("topic", function.Topic).Msg("Reloaded function")

				// Start a new subscription for created functions
				if event.Op&fsnotify.Create != 0 {
					go func(f JavaScriptFunction) {
						if err := subscribeToRedis(ctx, rdb, f, store); err != nil {
							appLogger.Error().Err(err).Str("topic", f.Topic).Msg("Error in Redis subscription")
						}
					}(*function)
				}
			}

			if event.Op&fsnotify.Remove != 0 {
				// Get the topic from the path
				relPath, err := filepath.Rel(root, filepath.Dir(event.Name))
				if err != nil {
					appLogger.Error().Err(err).Str("path", event.Name).Msg("Error getting relative path")
					continue
				}
				prefix := "functions/"
				if strings.HasPrefix(relPath, prefix) {
					relPath = relPath[len(prefix):]
				}
				store.Delete(relPath)
				appLogger.Info().Str("topic", relPath).Msg("Removed function")
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			appLogger.Error().Err(err).Msg("Watcher error")
		case <-ctx.Done():
			return nil
		}
	}
}
func subscribeToRedis(ctx context.Context, client *redis.Client, function JavaScriptFunction, store *FunctionStore) error {
	pubsub := client.Subscribe(ctx, function.Topic)
	defer pubsub.Close()

	ch := pubsub.Channel()

	appLogger.Info().Str("topic", function.Topic).Msg("Listening for messages")

	for msg := range ch {
		// Get the latest version of the function
		function, ok := store.Get(function.Topic)
		if !ok {
			appLogger.Info().Str("topic", function.Topic).Msg("Function no longer exists")
			return nil
		}

		// Extract correlation ID and unwrap content
		content, correlationID := unwrapMessage(msg.Payload)

		// Start execution logging
		executionID := generateExecutionID()
		startTime := time.Now()

		// Create a logger for this execution
		execLogger := messageLogger.With().
			Str("correlation_id", correlationID).
			Str("execution_id", executionID).
			Str("topic", function.Topic).
			Time("start_time", startTime).
			Logger()

		execLogger.Info().Str("content", content).Msg("Starting function execution")

		// Collect console output
		var consoleOutput []string

		// Create a new runtime for each execution to avoid state persistence
		rt := goja.New()

		// Add these right after setting up the publish function
		// Set up Redis get/set functions
		rt.Set("retrieveKey", func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) != 1 {
				panic(rt.NewTypeError("redisGet requires one argument: key"))
			}

			key := call.Arguments[0].String()

			result, err := client.Get(ctx, key).Result()
			if err == redis.Nil {
				return goja.Null()
			}
			if err != nil {
				execLogger.Error().Err(err).Str("key", key).Msg("Failed to get Redis key")
				panic(rt.NewTypeError(fmt.Sprintf("failed to get key: %v", err)))
			}

			execLogger.Info().Str("key", key).Msg("Retrieved Redis key")
			return rt.ToValue(result)
		})

		rt.Set("storeKey", func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) != 2 {
				panic(rt.NewTypeError("redisSet requires two arguments: key and value"))
			}

			key := call.Arguments[0].String()
			value := call.Arguments[1].String()

			err := client.Set(ctx, key, value, 0).Err() // 0 means no expiration
			if err != nil {
				execLogger.Error().Err(err).Str("key", key).Msg("Failed to set Redis key")
				panic(rt.NewTypeError(fmt.Sprintf("failed to set key: %v", err)))
			}

			execLogger.Info().Str("key", key).Msg("Set Redis key")
			return goja.Undefined()
		})

		// Set up publish function with correlation ID
		rt.Set("publish", func(call goja.FunctionCall) goja.Value {
			if len(call.Arguments) != 2 {
				panic(rt.NewTypeError("publish requires two arguments: topic and message"))
			}

			topic := call.Arguments[0].String()
			message := call.Arguments[1].String()

			// Wrap the message with correlation ID
			wrappedMessage := wrapMessage(message, correlationID)

			execLogger.Info().
				Str("publish_topic", topic).
				Str("message", message).
				Msg("Publishing message")

			err := client.Publish(ctx, topic, wrappedMessage).Err()
			if err != nil {
				execLogger.Error().Err(err).Msg("Failed to publish message")
				panic(rt.NewTypeError(fmt.Sprintf("failed to publish message: %v", err)))
			}

			return goja.Undefined()
		})

		// Set up console.log and other console methods with correlation ID
		console := map[string]interface{}{
			"log": func(call goja.FunctionCall) goja.Value {
				args := make([]interface{}, len(call.Arguments))
				for i, arg := range call.Arguments {
					args[i] = arg.String()
				}
				output := fmt.Sprint(args...)
				consoleOutput = append(consoleOutput, output)
				execLogger.Info().
					Str("level", "log").
					Str("output", output).
					Msg("Console output")
				return goja.Undefined()
			},
			"error": func(call goja.FunctionCall) goja.Value {
				args := make([]interface{}, len(call.Arguments))
				for i, arg := range call.Arguments {
					args[i] = arg.String()
				}
				output := fmt.Sprint(args...)
				consoleOutput = append(consoleOutput, "ERROR: "+output)
				execLogger.Error().
					Str("level", "error").
					Str("output", output).
					Msg("Console output")
				return goja.Undefined()
			},
			"warn": func(call goja.FunctionCall) goja.Value {
				args := make([]interface{}, len(call.Arguments))
				for i, arg := range call.Arguments {
					args[i] = arg.String()
				}
				output := fmt.Sprint(args...)
				consoleOutput = append(consoleOutput, "WARN: "+output)
				execLogger.Warn().
					Str("level", "warn").
					Str("output", output).
					Msg("Console output")
				return goja.Undefined()
			},
		}
		rt.Set("console", console)

		// Set up the message object with unwrapped content
		messageObj := map[string]interface{}{
			"payload": content,
			"topic":   function.Topic,
		}
		rt.Set("message", messageObj)

		// Execute the precompiled program
		_, err := rt.RunProgram(function.Program)
		execDuration := time.Since(startTime)

		if err != nil {
			execLogger.Error().
				Err(err).
				Dur("duration", execDuration).
				Strs("console_output", consoleOutput).
				Msg("Function execution failed")
			continue
		}

		execLogger.Info().
			Dur("duration", execDuration).
			Strs("console_output", consoleOutput).
			Msg("Function execution completed")
	}

	return nil
}

type FunctionRequest struct {
	Topic string `json:"topic"`
	Code  string `json:"code"`
}

func setupHTTPServer(store *FunctionStore, ctx context.Context, rdb *redis.Client, watcher *fsnotify.Watcher) *echo.Echo {
	e := echo.New()

	// Middleware
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORS())

	// Deploy a new function
	e.POST("/api/functions", func(c echo.Context) error {
		var req FunctionRequest
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Invalid request: %v", err))
		}

		// Create functions directory if it doesn't exist
		funcPath := filepath.Join("functions", req.Topic)
		if err := os.MkdirAll(funcPath, 0755); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to create directory: %v", err))
		}

		// Write the function file
		filePath := filepath.Join(funcPath, "func.js")
		if err := ioutil.WriteFile(filePath, []byte(req.Code), 0644); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to write function: %v", err))
		}

		// Load and compile the function
		function, err := loadJavaScriptFunction(filePath, "functions")
		if err != nil {
			// Clean up the file if compilation fails
			os.RemoveAll(funcPath)
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("Failed to compile function: %v", err))
		}

		// Store the function
		// Store the function and start subscription
		store.Set(function.Topic, *function)

		// Add the new directory to the watcher
		if err := watcher.Add(funcPath); err != nil {
			appLogger.Warn().Err(err).Msg("Failed to watch new function directory")
		}

		// Start Redis subscription for the new function
		go func(f JavaScriptFunction) {
			err := subscribeToRedis(ctx, rdb, f, store)
			if err != nil {
				appLogger.Error().Err(err).Str("topic", f.Topic).Msg("Error in Redis subscription")
			}
		}(*function)

		return c.JSON(http.StatusCreated, map[string]string{
			"status": "deployed",
			"topic":  function.Topic,
		})
	})

	// Remove a function
	e.DELETE("/api/functions/:topic", func(c echo.Context) error {
		topic := c.Param("topic")
		if topic == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "Topic not specified")
		}

		// Clean up resources
		if _, ok := store.Get(topic); ok {
			store.Delete(topic)

			// Remove the function directory
			funcPath := filepath.Join("functions", topic)
			if err := os.RemoveAll(funcPath); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to remove function: %v", err))
			}

			// Remove the directory from watcher
			// Note: fsnotify will automatically remove watches for deleted directories
			appLogger.Info().Str("topic", topic).Msg("Removed function and cleaned up resources")
		} else {
			return echo.NewHTTPError(http.StatusNotFound, "Function not found")
		}

		return c.JSON(http.StatusOK, map[string]string{
			"status": "removed",
			"topic":  topic,
		})
	})

	// List all functions
	e.GET("/api/functions", func(c echo.Context) error {
		store.RLock()
		functions := make([]string, 0, len(store.functions))
		for topic := range store.functions {
			functions = append(functions, topic)
		}
		store.RUnlock()

		return c.JSON(http.StatusOK, map[string]interface{}{
			"functions": functions,
		})
	})

	// Get function code
	e.GET("/api/functions/:topic", func(c echo.Context) error {
		topic := c.Param("topic")
		if topic == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "Topic not specified")
		}

		function, exists := store.Get(topic)
		if !exists {
			return echo.NewHTTPError(http.StatusNotFound, "Function not found")
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"topic": topic,
			"code":  function.Code,
		})
	})

	return e
}

func main() {
	// Set up logging
	if err := setupLogging(); err != nil {
		fmt.Printf("Failed to setup logging: %v\n", err)
		os.Exit(1)
	}

	// Set up Redis client
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})

	ctx := context.Background()

	// Test Redis connection
	_, err := rdb.Ping(ctx).Result()
	if err != nil {
		appLogger.Fatal().Err(err).Msg("Failed to connect to Redis")
	}

	// Create function store
	store := NewFunctionStore()

	// Find all JavaScript functions
	functions, err := findJavaScriptFiles("./functions")
	if err != nil {
		appLogger.Fatal().Err(err).Msg("Error finding JavaScript files")
	}

	// Initialize the store
	for _, function := range functions {
		store.Set(function.Topic, function)
	}

	appLogger.Info().Int("count", len(functions)).Msg("Found JavaScript functions")

	// Create watcher for file changes
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		appLogger.Fatal().Err(err).Msg("Failed to create watcher")
	}
	defer watcher.Close()

	// Start the file watcher
	go func() {
		if err := watchFunctions(ctx, "./functions", store, rdb); err != nil {
			appLogger.Error().Err(err).Msg("Error in file watcher")
		}
	}()

	// Subscribe to Redis topics for each function
	for _, function := range functions {
		go func(f JavaScriptFunction) {
			err := subscribeToRedis(ctx, rdb, f, store)
			if err != nil {
				appLogger.Error().Err(err).Str("topic", f.Topic).Msg("Error in Redis subscription")
			}
		}(function)
	}

	// Start HTTP server
	e := setupHTTPServer(store, ctx, rdb, watcher)
	appLogger.Info().Msg("HTTP server listening on :8080")
	if err := e.Start(":8080"); err != nil && err != http.ErrServerClosed {
		appLogger.Fatal().Err(err).Msg("HTTP server error")
	}
}

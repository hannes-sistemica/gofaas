# GoFaaS - Go Function as a Service with Redis

GoFaaS is a lightweight Function-as-a-Service (FaaS) platform that uses Redis pub/sub for message routing and JavaScript for function execution. It provides a simple way to deploy and manage serverless functions that can react to messages, process data, and maintain state using Redis.

## Features

- 🚀 JavaScript function execution
- 📨 Redis pub/sub based message routing
- 💾 Redis key-value storage capabilities
- 🔄 Hot-reloading of functions
- 🌐 REST API for function management
- 📝 Structured logging with correlation IDs
- 🔍 Log rotation and management

## Installation

### Prerequisites

- Go 1.17 or later
- Redis Server (or Docker)
- Git

### Setting up Redis with Docker

```bash
# Start Redis
docker run --name redis -p 6379:6379 -d redis

# Connect to Redis CLI
docker exec -it redis redis-cli
```

### Building GoFaaS

```bash
git clone https://github.com/yourusername/gofaas
cd gofaas
go mod download
go build
```

## Function Examples

### 1. Echo Function
Echoes a message to a specified topic with uppercase content.

```javascript
// functions/default/echo/func.js
(function() {
    try {
        const data = JSON.parse(message.payload);
        if (!data.topic || !data.content) {
            console.error("Message must contain 'topic' and 'content' fields");
            return;
        }
        console.log(`Echoing message to topic: ${data.topic}`);
        console.log(`Content:`, data.content);
        
        const jsonContent = JSON.stringify({ content: data.content.toUpperCase() });
        publish(data.topic, jsonContent);
    } catch (error) {
        console.error("Error processing message:", error.message);
    }
})();

// Test with:
redis-cli PUBLISH "default/echo" '{"topic": "default/sink", "content": "Hello, World!"}'
```

### 2. Sink Function
Simple message receiver that logs messages.

```javascript
// functions/default/sink/func.js
(function() {
    try {
        const data = JSON.parse(message.payload);
        if (!data.content) {
            console.error("Message must contain 'content' field");
            return;
        }
        console.log(`Content: ${data.content}`);
    } catch (error) {
        console.error("Error processing message:", error.message);
    }
})();

// Test with:
redis-cli PUBLISH "default/sink" '{"content": "Direct message to sink"}'
```

### 3. Counter Function
Maintains counters in Redis.

```javascript
// functions/default/counter/func.js
(function() {
    try {
        const data = JSON.parse(message.payload);
        const counterKey = "counter:" + (data.name || "default");
        
        // Get current value or start at 0
        let currentValue = retrieveKey(counterKey);
        let count = currentValue ? parseInt(currentValue) : 0;
        count++;
        
        // Store new value
        storeKey(counterKey, count.toString());
        
        // Publish result
        publish("counter/result", JSON.stringify({
            name: data.name,
            count: count
        }));
        
        console.log(`Incremented counter ${counterKey} to ${count}`);
    } catch (error) {
        console.error("Error in counter function:", error.message);
    }
})();

// Test with:
redis-cli PUBLISH "default/counter" '{"name": "visitors"}'
```

### 4. Cache Function
Key-value cache using Redis.

```javascript
// functions/default/cache/func.js
(function() {
    try {
        const data = JSON.parse(message.payload);
        
        if (data.action === "set") {
            storeKey(data.key, JSON.stringify(data.value));
            console.log(`Stored value for key: ${data.key}`);
            publish("cache/result", JSON.stringify({
                status: "stored",
                key: data.key
            }));
        } else if (data.action === "get") {
            const value = retrieveKey(data.key);
            console.log(`Retrieved value for key: ${data.key}`);
            publish("cache/result", JSON.stringify({
                status: "retrieved",
                key: data.key,
                value: value ? JSON.parse(value) : null
            }));
        }
    } catch (error) {
        console.error("Error in cache function:", error.message);
    }
})();

// Test with:
redis-cli PUBLISH "default/cache" '{"action": "set", "key": "user:123", "value": {"name": "John", "age": 30}}'
redis-cli PUBLISH "default/cache" '{"action": "get", "key": "user:123"}'
```

## REST API

### Function Management

```bash
# Deploy a function
curl -X POST http://localhost:8080/api/functions \
  -H "Content-Type: application/json" \
  -d '{
    "topic": "default/newfunction",
    "code": "your-function-code-here"
  }'

# List functions
curl http://localhost:8080/api/functions

# Get function code
curl http://localhost:8080/api/functions/default/newfunction

# Delete function
curl -X DELETE http://localhost:8080/api/functions/default/newfunction
```

## JavaScript Function Capabilities

Functions have access to:

1. `console` methods:
    - `console.log()`
    - `console.error()`
    - `console.warn()`

2. Redis operations:
    - `retrieveKey(key)`: Get value from Redis
    - `storeKey(key, value)`: Set value in Redis
    - `publish(topic, message)`: Publish message to Redis topic

3. Message context:
    - `message.payload`: The message content
    - `message.topic`: The current topic

## Logging

- Application logs: `logs/app.log`
- Message execution logs: `logs/messages.log`
- Log rotation: 10MB files, 3 backups, 28 days retention

## Future Enhancements

1. Additional Features:
    - REST endpoints for log viewing/searching
    - Message publishing via REST API
    - Webhook support for external integrations
    - Function timeouts and resource limits
    - Function chaining and workflows
    - Message batching and scheduling

2. Monitoring & Debug:
    - Function metrics (execution time, error rate)
    - Redis metrics dashboard
    - Real-time function logs viewing

3. Security:
    - Authentication/Authorization
    - Function isolation
    - Rate limiting
    - Input validation

## Use Cases

1. Event Processing
    - IoT device message handling
    - Webhook processors
    - Event transformation and routing

2. Data Processing
    - Log aggregation and processing
    - Data transformation
    - Stream processing

3. Integration
    - Microservice communication
    - API integration
    - Webhook handling

4. State Management
    - Counters and metrics
    - Caching
    - Session management

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

MIT License
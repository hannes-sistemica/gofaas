// Example func.js file that could be placed in functions/default/helloworld/func.js

// The message object is automatically injected and contains:
// - message.payload: The message content
// - message.topic: The topic name

console.log(`Executing function for topic: ${message.topic}`);
console.log(`Received message: ${message.payload}`);

// Process the message
const data = JSON.parse(message.payload);
// Add your processing logic here
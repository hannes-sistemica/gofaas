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
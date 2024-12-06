// functions/default/cache/func.js
(function() {
    try {
        const data = JSON.parse(message.payload);

        if (data.action === "set") {
            // Store value
            storeKey(data.key, JSON.stringify(data.value));
            console.log(`Stored value for key: ${data.key}`);
            publish("cache/result", JSON.stringify({
                status: "stored",
                key: data.key
            }));
        } else if (data.action === "get") {
            // Retrieve value
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
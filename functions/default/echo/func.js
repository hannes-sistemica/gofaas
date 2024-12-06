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

        // Ensure content is published as JSON string
        const jsonContent = JSON.stringify({
            content: data.content.toUpperCase()
        });
        publish(data.topic, jsonContent);

    } catch (error) {
        console.error("Error processing message:", error.message);
    }
})();
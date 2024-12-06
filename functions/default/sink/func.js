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
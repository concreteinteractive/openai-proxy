package main

import (
    "bufio"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "os"
    "strings"
    "time"

    "github.com/gin-contrib/cors"
    "github.com/gin-gonic/gin"
)

func main() {
    router := gin.Default()

    // Configure CORS middleware
    router.Use(cors.New(cors.Config{
        AllowOrigins:     []string{"http://localhost:5721"},
        AllowMethods:     []string{"POST", "OPTIONS"},
        AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
        ExposeHeaders:    []string{"Content-Length"},
        AllowCredentials: true,
        MaxAge:           12 * time.Hour,
    }))

    router.POST("/message", func(c *gin.Context) {
        var reqBody struct {
            ThreadID string `json:"thread_id"`
            Messages []struct {
                Role    string `json:"role"`
                Content string `json:"content"`
            } `json:"messages"`
        }
        if err := c.ShouldBindJSON(&reqBody); err != nil {
            c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
            return
        }

        fmt.Println("Received request:", reqBody)

        apiKey := os.Getenv("OPENAI_API_KEY")
        assistantID := os.Getenv("ASSISTANT_ID")
        if apiKey == "" || assistantID == "" {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "Missing API key or assistant ID"})
            return
        }

        client := &http.Client{}
        requestPayload := map[string]interface{}{
            "assistant_id": assistantID,
            "thread": map[string]interface{}{
                "messages": reqBody.Messages,
            },
            "stream": true,
        }
        if reqBody.ThreadID != "" {
            requestPayload["thread_id"] = reqBody.ThreadID
        }
        reqBodyBytes, _ := json.Marshal(requestPayload)

        openaiAPIURL := "https://api.openai.com/v1/threads/runs"
        fmt.Println("OpenAI API URL:", openaiAPIURL)
        req, err := http.NewRequest("POST", openaiAPIURL, strings.NewReader(string(reqBodyBytes)))
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
            return
        }

        req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiKey))
        req.Header.Set("Content-Type", "application/json")
        req.Header.Set("OpenAI-Beta", "assistants=v2") // Add the required header for v2

        resp, err := client.Do(req)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
            return
        }
        defer resp.Body.Close()

        if resp.StatusCode != http.StatusOK {
            var errorResponse struct {
                Error struct {
                    Message string `json:"message"`
                    Type    string `json:"type"`
                    Param   string `json:"param"`
                    Code    string `json:"code"`
                } `json:"error"`
            }
            json.NewDecoder(resp.Body).Decode(&errorResponse)
            c.JSON(resp.StatusCode, gin.H{"error": errorResponse.Error})
            return
        }

        reader := bufio.NewReader(resp.Body)
        var threadID string

        c.Stream(func(w io.Writer) bool {
            for {
                line, err := reader.ReadString('\n')
                if err != nil {
                    if err == io.EOF {
                        break
                    }
                    c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
                    return false
                }

                fmt.Println("Received line:", line) // Print the raw data line

                if strings.HasPrefix(line, "event: thread.run.created") {
                    dataLine, err := reader.ReadString('\n')
                    if err != nil {
                        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
                        return false
                    }

                    dataLine = strings.TrimPrefix(dataLine, "data: ")
                    dataLine = strings.TrimSpace(dataLine)

                    var runCreatedEvent struct {
                        ThreadID string `json:"thread_id"`
                    }
                    err = json.Unmarshal([]byte(dataLine), &runCreatedEvent)
                    if err != nil {
                        fmt.Println("Unmarshal error:", err)
                        fmt.Println("Data line:", dataLine)
                        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
                        return false
                    }
                    threadID = runCreatedEvent.ThreadID

                    // Send the thread ID to the client immediately
                    fmt.Fprintf(w, "{\"thread_id\":\"%s\"}\n", threadID)
                    c.Writer.Flush()
                }

                if strings.HasPrefix(line, "event: thread.message.delta") {
                    dataLine, err := reader.ReadString('\n')
                    if err != nil {
                        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
                        return false
                    }

                    if strings.HasPrefix(dataLine, "data: ") {
                        dataLine = strings.TrimPrefix(dataLine, "data: ")
                        dataLine = strings.TrimSpace(dataLine)

                        fmt.Println("Processing data line:", dataLine) // Print the data line being processed

                        var delta struct {
                            Delta struct {
                                Content []struct {
                                    Index int `json:"index"`
                                    Type  string `json:"type"`
                                    Text  struct {
                                        Value string `json:"value"`
                                    } `json:"text"`
                                } `json:"content"`
                            } `json:"delta"`
                        }

                        err = json.Unmarshal([]byte(dataLine), &delta)
                        if err != nil {
                            fmt.Println("Unmarshal error:", err)
                            fmt.Println("Data line:", dataLine)
                            c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
                            return false
                        }

                        for _, content := range delta.Delta.Content {
                            if content.Type == "text" {
                                fmt.Fprintf(w, "%s", content.Text.Value)
                                c.Writer.Flush()
                            }
                        }
                    }
                }

                if strings.HasPrefix(line, "event: thread.run.completed") || strings.HasPrefix(line, "event: done") {
                    // Close the connection after completion or when done
                    c.Writer.Flush()
                    return false
                }
            }
            return true
        })
    })

    router.Run(":8080")
}

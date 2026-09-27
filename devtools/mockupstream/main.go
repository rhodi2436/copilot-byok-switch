// 模拟 OpenAI 兼容上游，用于 cops 端到端冒烟测试。
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	port := "9911"
	name := "mock-a"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}
	if len(os.Args) > 2 {
		name = os.Args[2]
	}
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		model, _ := body["model"].(string)
		stream, _ := body["stream"].(bool)
		auth := r.Header.Get("Authorization")
		log.Printf("[%s] model=%s stream=%v auth=%s", name, model, stream, auth)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			chunks := []string{
				fmt.Sprintf(`data: {"id":"1","model":%q,"choices":[{"delta":{"content":"hello-from-%s"}}]}`, model, name),
				fmt.Sprintf(`data: {"id":"1","model":%q,"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50}}`, model),
				"data: [DONE]",
			}
			for _, c := range chunks {
				fmt.Fprint(w, c+"\n\n")
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"1","model":%q,"choices":[{"message":{"role":"assistant","content":"hello-from-%s"}}],"usage":{"prompt_tokens":100,"completion_tokens":50}}`, model, name)
	})
	http.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"mock-a","object":"model"},{"id":"mock-b","object":"model"}]}`)
	})
	log.Printf("mock upstream %s listening on :%s", name, port)
	log.Fatal(http.ListenAndServe("127.0.0.1:"+port, nil))
}

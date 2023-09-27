package main

import (
	"fmt"
	"io"
	"net/http"
)

func getRoot(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)

	if err != nil {
		fmt.Printf("errReadAll: %s\n", err)
	} else {
		l := len(body)
		fmt.Printf("got / request of %d bytes\n", l)
	}
	io.WriteString(w, "")
}

func main() {
	http.HandleFunc("/", getRoot)

	err := http.ListenAndServe(":9099", nil)
	if err != nil {
		fmt.Printf("err: %s\n", err)
	}
}

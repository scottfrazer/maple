package main

import "bytes"
import "fmt"
import "io"
import "os"

func main() {
	var b bytes.Buffer
	multi := io.MultiWriter(&b, os.Stdout)
	fmt.Fprintf(multi, "each of these strings\n")
	fmt.Fprintf(multi, "might become very large\n")
	fmt.Fprintf(multi, "and there are many of them\n")
	fmt.Println(b.String())
}

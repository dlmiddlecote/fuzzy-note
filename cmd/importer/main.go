package main

import (
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"
	"strings"

	"fuzzy-note/pkg/service"
)

const (
	fileName     = "workflowy_source.xml"
	nodeTitle    = "outline"
	rootFileName = "primary.db"
)

type Text struct {
	XMLName xml.Name
	Year    xml.Attr `xml:"startYear,attr"`
	Month   xml.Attr `xml:"startMonth,attr"`
	Day     xml.Attr `xml:"startDay,attr"`
	Content string   `xml:",any"`
}

type Node struct {
	XMLName xml.Name
	Text    xml.Attr   `xml:"text,attr"`
	Note    xml.Attr   `xml:"_note,attr"`
	Nodes   []Node     `xml:",any"`
	Attrs   []xml.Attr `xml:"-"`
}

func walk(db service.ListRepo, nodes []Node, chain []string, f func(Node) bool) {
	// Iterate over in reverse order to mimic real life entry
	for i := len(nodes) - 1; i >= 0; i-- {
		n := nodes[i]
		newChain := chain
		if f(n) {
			newText := n.Text.Value
			if len(newText) > 0 {
				t := Text{}
				xml.Unmarshal([]byte(newText), &t)
				if len(t.Year.Value) > 0 {
					fmt.Printf("BOOM %v\n", t.Content)
					fmt.Printf("BOOM %v\n", t.Year)
					newText = t.Content
				}
				//err := xml.Unmarshal([]byte(newText), &t)
				//if err == nil || err == io.EOF {
				//    if len(t.Year.Value) > 0 {
				//        fmt.Printf("BOOM %v\n", t.Content)
				//        newText = t.Content
				//    }
				//} else {
				//    log.Println(err)
				//}
			}
			newChain = append(newChain, newText)
		}
		walk(db, n.Nodes, newChain, f)
		fullString := strings.Join(newChain, " >> ")
		byteNote := []byte(n.Note.Value)
		err := db.Add(fullString, &byteNote, nil)
		if err != nil {
			log.Fatal(err)
			os.Exit(1)
		}
	}
}

func importLines(db service.ListRepo) error {
	err := db.Load() // Instantiate db
	if err != nil {
		log.Fatal(err)
		return err
	}

	dat, err := ioutil.ReadFile(fileName)
	if err != nil {
		log.Fatal(err)
		return err
	}

	n := Node{}
	err = xml.Unmarshal(dat, &n)
	if err != nil {
		log.Fatal(err)
		return err
	}

	walk(db, []Node{n}, []string{}, func(n Node) bool {
		if n.XMLName.Local == nodeTitle {
			return true
		}
		return false
	})

	err = db.Save()
	if err != nil {
		log.Fatal(err)
		return err
	}

	return nil
}

func main() {
	var rootDir, notesSubDir string
	if rootDir = os.Getenv("FZN_IMPORT_ROOT_DIR"); rootDir == "" {
		// TODO currently only works on OSs with HOME
		rootDir = path.Join(os.Getenv("HOME"), ".fzn/import/")
	}
	if notesSubDir = os.Getenv("FZN_NOTES_SUBDIR"); notesSubDir == "" {
		notesSubDir = "notes"
	}

	rootPath := path.Join(rootDir, rootFileName)
	notesDir := path.Join(rootDir, notesSubDir)

	// Create (if not exists) the notes subdirectory
	os.MkdirAll(notesDir, os.ModePerm)

	listRepo := service.NewDBListRepo(rootPath, notesDir)

	err := importLines(listRepo)
	if err != nil {
		log.Fatal(err)
		os.Exit(1)
	}
}

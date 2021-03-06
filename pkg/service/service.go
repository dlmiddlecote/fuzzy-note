package service

import (
	"encoding/binary"
	//"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// This is THE date that Golang needs to determine custom formatting
const dateFormat string = "Mon, Jan 2, 2006"

type ListRepo interface {
	Load() error
	Save() error
	Add(line string, note *[]byte, item *ListItem) error
	Update(line string, note *[]byte, listItem *ListItem) error
	Delete(listItem *ListItem) error
	MoveUp(listItem *ListItem) (bool, error)
	MoveDown(listItem *ListItem) (bool, error)
	Match(keys [][]rune, active *ListItem, showHidden bool) ([]*ListItem, error)
	HasPendingChanges() bool
	GetMatchPattern(sub []rune) (matchPattern, int)
}

type DBListRepo struct {
	rootPath          string
	notesPath         string
	root              *ListItem
	nextID            uint32
	hasPendingChanges bool
	pendingDeletions  []*ListItem
}

type ListItem struct {
	Line        string
	Note        *[]byte
	IsHidden    bool
	parent      *ListItem
	child       *ListItem
	id          uint32
	matchParent *ListItem
	matchChild  *ListItem
}

// FileHeader will store the schema id, so we know which pageheader to use
type FileHeader struct {
	SchemaID uint32
}

type bits uint32

const (
	hidden bits = 1 << iota
)

func set(b, flag bits) bits    { return b | flag }
func clear(b, flag bits) bits  { return b &^ flag }
func toggle(b, flag bits) bits { return b ^ flag }
func has(b, flag bits) bool    { return b&flag != 0 }

type ItemHeader struct {
	PageID     uint32
	Metadata   bits
	FileID     uint32
	DataLength uint64
}

func NewDBListRepo(rootPath string, notesPath string) *DBListRepo {
	return &DBListRepo{
		rootPath:          rootPath,
		notesPath:         notesPath,
		hasPendingChanges: false,
	}
}

func (r *DBListRepo) Load() error {
	f, err := os.OpenFile(r.rootPath, os.O_CREATE, 0644)
	if err != nil {
		log.Fatal(err)
		return err
	}
	defer f.Close()

	// Retrieve first line from the file, which will be the oldest (and therefore bottom) entry
	var cur, oldest *ListItem

	r.nextID = 1

OuterLoop:
	for {

		header := ItemHeader{}
		err := binary.Read(f, binary.LittleEndian, &header)

		if err != nil {
			switch err {
			case io.EOF:
				break OuterLoop
			case io.ErrUnexpectedEOF:
				fmt.Println("binary.Read failed on page header:", err)
				return err
			}
		}

		// Initially we need to find the next available index for the ENTIRE dataset
		if header.PageID >= r.nextID {
			r.nextID = header.PageID + 1
		}

		data := make([]byte, header.DataLength)
		err = binary.Read(f, binary.LittleEndian, &data)

		if err != nil {
			switch err {
			case io.EOF:
				break OuterLoop
			case io.ErrUnexpectedEOF:
				fmt.Println("binary.Read failed on page header:", err)
				return err
			}
		}

		dat, err := r.loadPage(header.PageID)
		if err != nil {
			return err
		}

		nextItem := ListItem{
			Line:     string(data),
			parent:   cur,
			id:       header.PageID,
			Note:     dat,
			IsHidden: has(header.Metadata, hidden),
		}
		if cur == nil {
			// `cur` will only be nil on the first iteration, therefore we can assign the oldest node here for idx assignment below
			oldest = &nextItem
		}
		cur = &nextItem
	}

	// Handle empty file
	if cur == nil {
		return nil
	}

	// Now we have know the global nextID (to account for unordered IDs), iterate through (from oldest to youngest) and assign any indexes where required.
	for {
		if oldest.child == nil {
			break
		}
		if oldest.id == 0 {
			oldest.id = r.nextID
			r.nextID++
		}
		oldest = oldest.child
	}

	r.root = cur

	// `cur` is now a ptr to the most recent ListItem
	for {
		if cur.parent == nil {
			break
		}
		cur.parent.child = cur
		cur = cur.parent
	}

	return nil
}

func (r *DBListRepo) Save() error {
	for _, item := range r.pendingDeletions {
		// Because I don't yet trust the app, rather than deleting notes (which could be unintentionally
		// deleted with lots of data), append them with `_bak_{line}_{timestamp}`, so we know the context
		// of the line, and the timestamp at which it was deleted. We need to remove the originally named
		// notes file to prevent orphaned files being used with future notes (due to current idx logic)
		strID := fmt.Sprint(item.id)
		oldPath := path.Join(r.notesPath, strID)

		reg, err := regexp.Compile("[^a-zA-Z0-9]+")
		if err != nil {
			log.Fatal(err)
		}
		alphanumline := reg.ReplaceAllString(item.Line, "")

		newPath := path.Join(r.notesPath, fmt.Sprintf("bak_%d_%s_%s", item.id, alphanumline, fmt.Sprint(time.Now().Unix())))
		err = os.Rename(oldPath, newPath)
		if err != nil {
			if !os.IsNotExist(err) {
				log.Fatal(err)
				return err
			}
		}
	}

	r.pendingDeletions = []*ListItem{}
	// Account for edge condition where Load hasn't been run, and the id is incorrectly set to 0
	if r.nextID == 0 {
		r.nextID = 1
	}

	// TODO when appending individual item rather than overwriting
	//f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)

	f, err := os.Create(r.rootPath)
	if err != nil {
		log.Fatal(err)
		return err
	}
	defer f.Close()

	listItem := r.root

	// Write empty file if no listItems exist
	if listItem == nil {
		err := binary.Write(f, binary.LittleEndian, []byte{})
		if err != nil {
			fmt.Println("binary.Write failed:", err)
			log.Fatal(err)
			return err
		}
		r.hasPendingChanges = false
		return nil
	}

	r.root = listItem

	// TODO store oldest item on Load
	// Get oldest listItem
	for {
		if listItem.parent == nil {
			break
		}
		listItem = listItem.parent
	}

	for {
		if listItem.id == 0 {
			listItem.id = r.nextID
			r.nextID++
		}
		var metadata bits = 0
		if listItem.IsHidden {
			metadata = set(metadata, hidden)
		}
		header := ItemHeader{
			PageID:     listItem.id,
			Metadata:   metadata,
			FileID:     listItem.id, // TODO
			DataLength: uint64(len(listItem.Line)),
		}

		// TODO the below writes need to be atomic
		err := binary.Write(f, binary.LittleEndian, &header)
		if err != nil {
			fmt.Println("binary.Write failed:", err)
			log.Fatal(err)
			return err
		}
		data := []byte(listItem.Line)
		err = binary.Write(f, binary.LittleEndian, &data)
		if err != nil {
			fmt.Println("binary.Write failed:", err)
			log.Fatal(err)
			return err
		}
		r.savePage(listItem.id, listItem.Note)

		if listItem.child == nil {
			break
		}
		listItem = listItem.child
	}
	r.hasPendingChanges = false
	return nil
}

func (r *DBListRepo) Add(line string, note *[]byte, child *ListItem) error {
	r.hasPendingChanges = true

	if note == nil {
		note = &[]byte{}
	}
	newItem := ListItem{
		Line:  line,
		id:    r.nextID,
		child: child,
		Note:  note,
	}
	r.nextID++

	// If `child` is nil, it's the first item in the list so set as root and return
	if child == nil {
		oldRoot := r.root
		r.root = &newItem
		if oldRoot != nil {
			newItem.parent = oldRoot
			oldRoot.child = &newItem
		}
		return nil
	}

	if child.parent != nil {
		child.parent.child = &newItem
		newItem.parent = child.parent
	}
	child.parent = &newItem

	return nil
}

func (r *DBListRepo) Update(line string, note *[]byte, listItem *ListItem) error {
	listItem.Line = line
	listItem.Note = note
	r.hasPendingChanges = true
	return nil
}

func (r *DBListRepo) Delete(item *ListItem) error {
	r.hasPendingChanges = true

	if item.child != nil {
		item.child.parent = item.parent
	} else {
		// If the item has no child, it is at the top of the list and therefore we need to update the root
		r.root = item.parent
	}

	if item.parent != nil {
		item.parent.child = item.child
	}

	r.pendingDeletions = append(r.pendingDeletions, item)

	return nil
}

func (r *DBListRepo) MoveUp(item *ListItem) (bool, error) {
	r.hasPendingChanges = true

	targetItem := item.matchChild
	if targetItem == nil {
		return false, nil
	}

	// Close off gap from source location (for whole dataset)
	oldSourceParent := item.parent
	if oldSourceParent != nil {
		item.parent.child = item.child
	}
	if item.child != nil {
		item.child.parent = oldSourceParent
	}

	// Insert item into new position based on Matched pointers
	item.parent = targetItem
	if targetItem.child == nil {
		// New root
		item.child = nil
		r.root = item
	} else {
		item.child = targetItem.child
		targetItem.child.parent = item
	}
	targetItem.child = item

	return true, nil
}

func (r *DBListRepo) MoveDown(item *ListItem) (bool, error) {
	r.hasPendingChanges = true

	targetItem := item.matchParent
	if targetItem == nil {
		return false, nil
	}

	// Close off gap from source location (for whole dataset)
	oldSourceChild := item.child
	if oldSourceChild != nil {
		item.child.parent = item.parent
	}
	if item.parent != nil {
		item.parent.child = oldSourceChild
		if oldSourceChild == nil {
			r.root = item.parent
		}
	}

	// Insert item into new position based on Matched pointers
	item.child = targetItem
	if targetItem.parent != nil {
		targetItem.parent.child = item
	}
	item.parent = targetItem.parent
	targetItem.parent = item

	return true, nil
}

// Search functionality

func isSubString(sub string, full string) bool {
	if strings.Contains(strings.ToLower(full), strings.ToLower(sub)) {
		return true
	}
	return false
}

// Iterate through the full string, when you match the "head" of the sub rune slice,
// pop it and continue through. If you clear sub, return true. Searches in O(n)
func isFuzzyMatch(sub []rune, full string) bool {
	for _, c := range full {
		if unicode.ToLower(c) == unicode.ToLower(sub[0]) {
			_, sub = sub[0], sub[1:]
		}
		if len(sub) == 0 {
			return true
		}
	}
	return false
}

const (
	openOp  rune = '{'
	closeOp rune = '}'
)

type matchPattern int

const (
	fullMatchPattern matchPattern = iota
	inverseMatchPattern
	fuzzyMatchPattern
	opMatchPattern
	noMatchPattern
)

// matchChars represents the number of characters at the start of the string
// which are attributed to the match pattern.
// This is used elsewhere to strip the characters where appropriate
var matchChars = map[matchPattern]int{
	fullMatchPattern:    1,
	inverseMatchPattern: 2,
	fuzzyMatchPattern:   0,
	opMatchPattern:      1,
	noMatchPattern:      0,
}

// GetMatchPattern will return the matchPattern of a given string, if any, plus the number
// of chars that can be omitted to leave only the relevant text
func (r *DBListRepo) GetMatchPattern(sub []rune) (matchPattern, int) {
	if len(sub) == 0 {
		return noMatchPattern, 0
	}
	pattern := fuzzyMatchPattern
	if sub[0] == '#' {
		if len(sub) > 1 {
			// Inverse string match if a search group begins with `#!`
			if sub[1] == '!' {
				pattern = inverseMatchPattern
			}
		}
		pattern = fullMatchPattern
	} else if sub[0] == openOp {
		pattern = opMatchPattern
	}
	nChars, _ := matchChars[pattern]
	return pattern, nChars
}

func parseOperatorGroup(sub []rune, nChars int) []rune {
	// Handle generic ops between the operator boundary runes
	// Get operator
	opRunes := []rune{}
	for _, r := range sub[nChars:] {
		if r == closeOp {
			// Match the op against any known operator (e.g. date) and parse if applicable.
			// TODO for now, just match `d` or `D` for date, we'll expand in the future.
			if len(opRunes) == 1 && unicode.ToLower(opRunes[len(opRunes)-1]) == 'd' {
				// Parse date string and return cast rune array
				now := time.Now()
				dateString := now.Format(dateFormat)
				return []rune(dateString)
			}
			break
		}
		opRunes = append(opRunes, r)
	}
	return sub
}

// If a matching group starts with `#` do a substring match, otherwise do a fuzzy search
func isMatch(sub []rune, full string, pattern matchPattern, nChars int) bool {
	if len(sub) == 0 {
		return true
	}
	switch pattern {
	case fullMatchPattern:
		return isSubString(string(sub[nChars:]), full)
	case inverseMatchPattern:
		return !isSubString(string(sub[nChars:]), full)
	case fuzzyMatchPattern:
		return isFuzzyMatch(sub, full)
	default:
		// Shouldn't reach here
		return false
	}
}

func (r *DBListRepo) Match(keys [][]rune, active *ListItem, showHidden bool) ([]*ListItem, error) {
	/*For each line, iterate through each searchGroup. We should be left with lines with fulfil all groups. */
	cur := r.root
	var lastCur *ListItem

	res := []*ListItem{}

	if cur == nil {
		return res, nil
	}

	// We need to pre-process the keys to parse any operators. We can't do this in the same loop as when
	// we have no matching lines, the parsing logic will not be reached, and things get messy

	for i, group := range keys {
		// Check if we need to parse any operators
		pattern, nChars := r.GetMatchPattern(group)
		if pattern == opMatchPattern {
			group = parseOperatorGroup(group, nChars)
			// TODO Confirm: The slices within the slice appear to be the same mem locations as those
			// passed in so they mutate as needed
			keys[i] = group
		}
	}

	for {
		if showHidden || !cur.IsHidden {
			matched := true
			for _, group := range keys {
				// Match any items with empty Lines (this accounts for lines added when search is active)
				// "active" listItems pass automatically to allow mid-search item editing
				if len(cur.Line) == 0 || cur == active {
					break
				}
				// TODO unfortunate reuse of vars - refactor to tidy
				pattern, nChars := r.GetMatchPattern(group)
				if !isMatch(group, cur.Line, pattern, nChars) {
					matched = false
					break
				}
			}
			if matched {
				res = append(res, cur)

				if lastCur != nil {
					lastCur.matchParent = cur
				}
				cur.matchChild = lastCur
				lastCur = cur
			}
		}

		if cur.parent == nil {
			return res, nil
		}

		cur = cur.parent
	}
}

func (r *DBListRepo) loadPage(id uint32) (*[]byte, error) {
	strID := fmt.Sprint(id)
	filePath := path.Join(r.notesPath, strID)

	dat := make([]byte, 0)
	// If file does not exist, return nil
	if _, err := os.Stat(filePath); err != nil {
		if os.IsNotExist(err) {
			return &dat, nil
		} else {
			return nil, err
		}
	}

	// Read whole file
	dat, err := ioutil.ReadFile(filePath)
	if err != nil {
		log.Fatal(err)
		return nil, err
	}
	return &dat, nil
}

func (r *DBListRepo) savePage(id uint32, data *[]byte) error {
	strID := fmt.Sprint(id)
	filePath := path.Join(r.notesPath, strID)

	// If data has been removed or is empty, delete the file and return
	if data == nil || len(*data) == 0 {
		_ = os.Remove(filePath)
		// TODO handle failure more gracefully? AFAIK os.Remove just returns a *PathError on failure
		// which is mostly indicative of a noneexistent file, so good enough for now...
		return nil
	}

	// Open or create a file in the `/notes/` subdir using the listItem id as the file name
	// This needs to be before the ReadFile below to ensure the file exists
	f, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	defer f.Close()

	_, err = f.Write(*data)
	if err != nil {
		fmt.Println("binary.Write failed:", err)
		return err
	}
	return nil
}

func (r *DBListRepo) HasPendingChanges() bool {
	return r.hasPendingChanges
}

package main

import (
	"encoding/json"
	"fmt"
	"github.com/fhs/gompd/mpd"
	"github.com/go-redis/redis/v8"
	// "github.com/kkdai/youtube/v2"
	"github.com/robfig/cron/v3"
	"github.com/rs/cors"
	"html/template"
	"log"
	"net/http"
	"strconv"

	"os"
	"time"
	"strings"
	"context"


	"path/filepath"
	"regexp"

	"io/ioutil"

	"os/exec"
	"math/rand"
	"sync"

)



type FileInfo struct {
	Directory    string                 `json:"directory,omitempty"`
	LastModified string                 `json:"last-modified"`
	Count        map[string]interface{} `json:"count,omitempty"`
	Artist       string                 `json:"artist,omitempty"`
	File         string                 `json:"file,omitempty"`
	Format       string                 `json:"format,omitempty"`
	Time         string                 `json:"time,omitempty"`

	Duration     string					`json:"duration,omitempty"`
	Genre        string					`json:"genre,omitempty"`
	Codec        string					`json:"codec,omitempty"`
	Track        string					`json:"track,omitempty"`
	Date         string					`json:"date,omitempty"`
	Album        string					`json:"album,omitempty"`
	Title        string					`json:"title,omitempty"`
	
	Stream       string                  `json:"stream,omitempty"`

}



var ctx = context.Background()




var (
	redisClient *redis.Client
	bandcamp_enabled = true
	mpdClient *MPDClientWrapper
	mpdClientPoll *MPDClientWrapper
	clientDB string
)


// Initialize appConfig using environment variables, defaulting to localhost if not set
var appConfig = struct {
    MPDHost string
}{
    MPDHost: getEnv("MPD_HOST", "localhost"),
}


func init() {
	clientDB = os.Getenv("CLIENT_DB")
	if clientDB == "" {
		clientDB = "/tmp/audioloader-db"
	}
}



// Helper function to get environment variables with a default value
func getEnv(key, defaultVal string) string {
    if value, exists := os.LookupEnv(key); exists {
        return value
    }
    return defaultVal
}

type MPDClientWrapper struct {
    client *mpd.Client
	mu     sync.Mutex
}


func NewMPDClientWrapper(address string) (*MPDClientWrapper, error) {
    client, err := mpd.Dial("tcp", address)
    if err != nil {
        return nil, err
    }
    return &MPDClientWrapper{client: client}, nil
}

func (w *MPDClientWrapper) Reconnect(address string) error {
    backoff := time.Second
    for {
        log.Println("Attempting to reconnect to MPD...")

        client, err := mpd.Dial("tcp", address)
        if err == nil {
            w.mu.Lock()
            w.client = client
            w.mu.Unlock()
            log.Println("Reconnected to MPD")
            return nil
        }

        log.Printf("Reconnect failed: %v. Retrying in %s...\n", err, backoff)
        time.Sleep(backoff)
        backoff *= 2
        if backoff > time.Minute {
            backoff = time.Minute
        }
    }
}

func (w *MPDClientWrapper) Client() *mpd.Client {
    w.mu.Lock()
    defer w.mu.Unlock()
    return w.client
}

func (w *MPDClientWrapper) EnsureConnection(address string) {
    go func() {
        for {
            w.mu.Lock()
            client := w.client
            w.mu.Unlock()

            if client == nil {
                log.Println("MPD client is nil. Attempting to reconnect...")
                if err := w.Reconnect(address); err != nil {
                    log.Printf("Failed to reconnect to MPD: %v", err)
                }
                continue
            }

            err := client.Ping()
            if err != nil {
                log.Println("MPD connection lost. Reconnecting...")
                if err := w.Reconnect(address); err != nil {
                    log.Printf("Failed to reconnect to MPD: %v", err)
                }
            }
            
            time.Sleep(10 * time.Second) // Adjust the interval as necessary
        }
    }()
}



func main() {

	// Initialize Redis client
	redisClient = redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   0,
	})

	// Initialize MPD client
	var err error
	mpdClient, err = NewMPDClientWrapper("localhost:6600")
	if err != nil {
		log.Fatalf("Failed to connect to MPD: %v", err)
	}
	defer mpdClient.Client().Close()

	mpdClient.EnsureConnection("localhost:6600")

	// Initialize Cron scheduler
	c := cron.New()
	c.Start()

	fs := http.FileServer(http.Dir("./static"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))

	// Set up HTTP routes
	http.HandleFunc("/cover", coverHandler)
	http.HandleFunc("/poll_currentsong", pollCurrentSongHandler)
	http.HandleFunc("/kodi", kodiHandler)
	http.HandleFunc("/upnp", upnpHandler)
	http.HandleFunc("/generate_randomset", generateRandomSetHandler)
	http.HandleFunc("/remove_favourite", favouritesHandler)
	http.HandleFunc("/add_favourite", favouritesHandler)
	http.HandleFunc("/active_players", activePlayersHandler)
	http.HandleFunc("/radio_history", radioHistoryHandler)
	http.HandleFunc("/bandcamp_history", bandcampHistoryHandler)
	http.HandleFunc("/history", dataHandler)
	http.HandleFunc("/randomset", dataHandler)
	http.HandleFunc("/favourites", dataHandler)
	http.HandleFunc("/search_radio", searchRadioHandler)
	http.HandleFunc("/search_bandcamp", searchBandcampHandler)
	http.HandleFunc("/remove_history", removeHistoryHandler)
	http.HandleFunc("/listfiles", mpdProxyHandler)
	http.HandleFunc("/lsinfo", mpdProxyHandler)
	http.HandleFunc("/ls", mpdProxyHandler)
	http.HandleFunc("/search", mpdProxyHandler)
	http.HandleFunc("/addplay", mpdProxyHandler)
	http.HandleFunc("/play", mpdProxyHandler)
	http.HandleFunc("/pause", mpdProxyHandler)
	http.HandleFunc("/playpause", mpdProxyHandler)
	http.HandleFunc("/next", mpdProxyHandler)
	http.HandleFunc("/prev", mpdProxyHandler)
	http.HandleFunc("/stop", mpdProxyHandler)
	http.HandleFunc("/status", mpdProxyHandler)
	http.HandleFunc("/currentsong", currentSongHandler)
	http.HandleFunc("/count", countHandler)
	http.HandleFunc("/toggleoutput", toggleOutputHandler)
	http.HandleFunc("/", catchAllHandler)

	// Enable CORS
	handler := cors.Default().Handler(http.DefaultServeMux)

	// Start HTTP server
	log.Println("Starting server on :8080")
	log.Fatal(http.ListenAndServe(":8080", handler))
}


func coverHandler(w http.ResponseWriter, r *http.Request) {
	directory := r.URL.Query().Get("directory")
	log.Printf("Getting cover for: %s", directory)

	responseType := r.URL.Query().Get("response_type")
	if responseType == "" {
		responseType = "direct"
	}
	cover := "vinyl.webp"

	// Connect to Redis
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	ctx := context.Background()

	// Attempt to get the cover from Redis
	val, err := rdb.Get(ctx, "audioloader:cover:"+directory).Result()
	if err == redis.Nil {
		cover = "vinyl.webp"
	} else if err != nil {
		log.Printf("Redis error: %v", err)
		cover = "vinyl.webp"
	} else {
		cover = val
		log.Printf("Got cover from Redis: %s", cover)
	}

	// If the cover is not found, search the MPD directory
	if cover == "vinyl.webp" || cover == "" {

		dirContent, err := mpdClient.Client().ListFiles(directory)
		if err != nil {
			log.Printf("Error listing files: %v", err)
			http.Error(w, "Failed to list files", http.StatusInternalServerError)
			return
		}
		
		imagePattern := regexp.MustCompile(`\.(jpg|jpeg|png|gif)$`)
		coverPattern := regexp.MustCompile(`(?i)folder|cover|front`)

		images := []string{}
		for _, fileData := range dirContent {
			file := fileData["file"]
			if imagePattern.MatchString(file) {
				images = append(images, file)
			}
		}


		log.Printf("Found images: %v", images)
		for _, image := range images {
			if coverPattern.MatchString(image) {
				cover = image
				break
			}
		}

		if cover == "vinyl.webp" && len(images) > 0 {
			cover = images[0]
		}

		log.Printf("Selected cover: %s", cover)

		// Save the cover in Redis
		if err := rdb.Set(ctx, "audioloader:cover:"+directory, cover, 0).Err(); err != nil {
			log.Printf("Error setting cover in Redis: %v", err)
		}
	}

	// Determine response
	if responseType == "redirect" {
		var fullPath string
		if cover == "vinyl.webp" {
			fullPath = "/static/assets/vinyl.webp"
		} else {
			fullPath = "/music" + "/" + directory + "/" + cover
		}
		http.Redirect(w, r, fullPath, http.StatusFound)
	} else {
		var coverPath string
		if cover == "vinyl.webp" {
			coverPath = "./static/assets/vinyl.webp"
		} else {
			libraryPath := os.Getenv("LIBRARY_PATH")
			if libraryPath == "" {
				libraryPath = "/home/kris/Music/opus"
			}
			coverPath = filepath.Join(libraryPath, directory, cover)		}
		if _, err := os.Stat(coverPath); os.IsNotExist(err) {
			log.Printf("Cover not found, falling back to default: %v", coverPath)
			coverPath = "./static/assets/vinyl.webp"
		}
		log.Printf("Serving cover: %v", coverPath)
		http.ServeFile(w, r, coverPath)
	}
}


type ClientData struct {
	History []string `json:"history,omitempty"`
	Randomset []string `json:"randomset,omitempty"`
	Favourites []string `json:"favourites,omitempty"`
	RadioHistory []string `json:"radio_history,omitempty"`
	BandcampHistory []string `json:"bandcamp_history,omitempty"`

}

// readData reads the client's data from a JSON file.
// readData reads client data from a JSON file.

// readData reads client data from a JSON file and returns a ClientData struct.
// readData reads client data from a JSON file and returns a ClientData struct.
func readData(clientID string, dataType string) (ClientData, error) {
	// Construct the file path
	clientDataFile := filepath.Join(clientDB, fmt.Sprintf("%s.%s.json", clientID, dataType))

	// Validate clientID and file path
	if clientID == "" || !filepath.HasPrefix(clientDataFile, clientDB) {
		return ClientData{}, fmt.Errorf("invalid clientID or file path")
	}

	// Check for invalid characters in the clientID
	if !regexp.MustCompile(`^[A-Za-z0-9_\-\.]+$`).MatchString(clientID) {
		return ClientData{}, fmt.Errorf("invalid characters in clientID")
	}

	// Read the file
	data, err := ioutil.ReadFile(clientDataFile)
	if err != nil {
		log.Printf("Warning: %s for %s not readable: %v\n", dataType, clientID, err)
		return ClientData{}, fmt.Errorf("not readable")
	}

	// Log the data as a string for debugging
	log.Printf("readData - data read: %s", string(data))

	// Unmarshal JSON into ClientData struct
	var clientdata ClientData
	if err := json.Unmarshal(data, &clientdata); err != nil {
		log.Printf("Error parsing JSON for %s: %v\n", clientID, err)
		return ClientData{}, fmt.Errorf("Cannot encode")
	}

	return clientdata, nil
}


func reverse(s []map[string]interface{}) []map[string]interface{} {
    a := make([]map[string]interface{}, len(s))
    for i, v := range s {
        a[len(s)-1-i] = v
    }
    return a
}

func getRadioStationURL(stationUUID string) string {
	// Placeholder for pyradios implementation
	// Replace this with the actual implementation later
	return ""
}

func pollCurrentSongHandler(w http.ResponseWriter, r *http.Request) {
	var err error
	
	log.Printf("Starting to poll")

	mpdClientPoll, err = NewMPDClientWrapper("localhost:6600")
	if err != nil {
		log.Fatalf("Failed to connect to MPD: %v", err)
	}
	defer mpdClientPoll.Client().Close()

	mpdClientPoll.EnsureConnection("localhost:6600")

    // Wait for MPD events with a timeout
    timeout := 180 * time.Second
    done := make(chan bool)
    go func() {
        result, err := mpdClientPoll.Client().Idle("playlist", "player")
        if err != nil {
            log.Printf("Failed to wait for MPD events: %v", err)
        } else {
			log.Printf("%s", result)
		}
        done <- true
    }()

	log.Printf("Back from poll")

    select {
    case <-done:
        // MPD event received
		log.Printf("Something happened")
    case <-time.After(timeout):
        // Timeout occurred
        log.Printf("Timeout waiting for MPD events")
    }

    // Get current song and status
    currentsong, err := mpdClient.Client().CurrentSong()


    if err != nil {
        // http.Error(w, "Failed to get current song", http.StatusInternalServerError)
        log.Printf("Failed to get current song in pollCurrentSongHandler: %v", err)
    }
	log.Printf("CurrentSong: %v", currentsong)
    
    status, err := mpdClient.Client().Status()
    if err != nil {
        // http.Error(w, "Failed to get status", http.StatusInternalServerError)
        log.Printf("Failed to get status in pollCurrentSongHandler: %v", err)
    }


	log.Printf("Status: %v", status)

    // Convert mpd.Attrs to map[string]interface{}
    currentsongMap := make(map[string]interface{})
    for k, v := range currentsong {
        currentsongMap[k] = v
    }
    for k, v := range status {
        currentsongMap[k] = v
    }

    // Add additional information
    currentsongMap["players"] = getActivePlayers()
    currentsongMap["bandcamp_enabled"] = bandcamp_enabled
    currentsongMap["default_stream"] = "http://" + os.Getenv("hostname") + ":8000/audio.ogg"

	log.Printf("currentsongMap: %v", currentsongMap)

    // Process current song
    currentsongMap = processCurrentSong(currentsongMap)

	log.Printf("currentsongMap g2: %v", currentsongMap)

    // Convert map[string]interface{} back to mpd.Attrs
    processedSong := make(mpd.Attrs)
    for k, v := range currentsongMap {
        processedSong[k] = fmt.Sprintf("%v", v)
    }

	log.Printf("Data return: %v", processedSong)

    // Return the content as JSON
    w.Header().Set("Content-Type", "application/json")

	encode_err := json.NewEncoder(w).Encode(processedSong)
	if encode_err != nil {
		// Log the error
		log.Printf("Error encoding JSON response: %v", encode_err)
		
		emptyResponse := map[string]interface{}{}
		json.NewEncoder(w).Encode(emptyResponse)
	}


}

func kodiHandler(w http.ResponseWriter, r *http.Request) {
	// Implement Kodi handler
}

func upnpHandler(w http.ResponseWriter, r *http.Request) {
	// Implement UPnP handler
}

func randomChoice(albums []string, k int) []string {
	n := len(albums)
	if k >= n {
		k = n
	}
	shuffled := make([]string, n)
	copy(shuffled, albums)
	rand.Shuffle(n, func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	return shuffled[:k]
}

func uniqueStrings(input []string) []string {
	uniqueMap := make(map[string]bool)
	var result []string
	for _, item := range input {
		if _, exists := uniqueMap[item]; !exists {
			uniqueMap[item] = true
			result = append(result, item)
		}
	}
	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func isValidFileName(fileName string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9_\-\.\/]+$`).MatchString(fileName)
}


func generateRandomSetHandler(w http.ResponseWriter, r *http.Request) {
	clientID := r.URL.Query().Get("client_id")
	
	filter := r.URL.Query().Get("set_filter")
	clientData := ClientData{Randomset: []string{}}
	artists := make(map[string]bool)

	albums, err := mpdClient.Client().List("album")
	if err != nil {
		log.Printf("Error listing albums: %v\n", err)
		http.Error(w, "failed to generate randomset", http.StatusInternalServerError)
		return
	}
	
	

	rand.Seed(time.Now().UnixNano()) // Seed for randomness

	for i := 0; len(clientData.Randomset) < 12 && i < 20; i++ {
		randomAlbums := randomChoice(albums, 12) // Get 12 random albums
		
		for _, album := range randomAlbums {
			albumData, err := mpdClient.Client().Search("album", album)
			if err != nil {
				log.Printf("Error searching album %s: %v\n", album, err)
				continue
			}

			if len(albumData) == 0 {
				continue
			}

			artist := albumData[0]["Artist"]
			if _, exists := artists[artist]; exists {
				continue
			}

			if filter == "" || !regexp.MustCompile(filter).MatchString(albumData[0]["file"]) {
				clientData.Randomset = append(clientData.Randomset, filepath.Dir(albumData[0]["file"]))
				artists[artist] = true
			}
		}
	}

	
	// Ensure randomset has unique albums and limit to 12
	clientData.Randomset = uniqueStrings(clientData.Randomset)[:min(12, len(clientData.Randomset))]
	log.Printf("clientdata unique - %v", clientData.Randomset)

	
	// Save to file
	clientDataFile := filepath.Join(clientDB, fmt.Sprintf("%s.randomset.json", clientID))

	log.Printf("clientDataFile - %v", clientDataFile)
	log.Printf("filepath.HasPrefix(clientDataFile, clientDB)- %v %v", filepath.HasPrefix(clientDataFile, clientDB), isValidFileName(clientDataFile))
	if clientID != "" && filepath.HasPrefix(clientDataFile, clientDB) && isValidFileName(clientDataFile) {
		file, err := os.Create(clientDataFile)
		log.Printf("clientDataFile created %v", clientDataFile)
		if err != nil {
			log.Printf("Error writing randomset file: %v\n", err)
			http.Error(w, "failed to save randomset", http.StatusInternalServerError)
			return
		}
		defer file.Close()

		encoder := json.NewEncoder(file)
		if err := encoder.Encode(clientData); err != nil {
			log.Printf("Error encoding JSON: %v\n", err)
			http.Error(w, "failed to save randomset", http.StatusInternalServerError)
			return
		}
	}

	// Send OK response
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"result": "ok"}`))


}

// WriteData writes client data to a JSON file.
func writeData(clientID, dataType string, clientData ClientData) error {
    clientDataFile := filepath.Join(clientDB, fmt.Sprintf("%s.%s.json", clientID, dataType))

    if clientID == "" || !filepath.HasPrefix(clientDataFile, clientDB) {
        return fmt.Errorf("invalid clientID or file path")
    }

    if !regexp.MustCompile(`^[A-Za-z0-9_\-\.]+$`).MatchString(clientID) {
        return fmt.Errorf("invalid characters in clientID")
    }

    data, err := json.Marshal(clientData)
    if err != nil {
        return err
    }

    return os.WriteFile(clientDataFile, data, 0644)
}

// FavouritesHandler handles adding or removing favourites.
func favouritesHandler(w http.ResponseWriter, r *http.Request) {
    clientID := r.URL.Query().Get("client_id")
    if clientID == "" {
        http.Error(w, "client_id is required", http.StatusBadRequest)
        return
    }

    directory := r.URL.Query().Get("directory")
    if directory == "" {
        directory = "."
    }

    // Read existing favourites data
    clientData, err := readData(clientID, "favourites")
    if err != nil {
        log.Printf("readData failed for favourites %s: %v\n", clientID, err)
        // http.Error(w, "Failed to load favourites", http.StatusInternalServerError)
        // return
    }

	if clientData.Favourites == nil {
        clientData.Favourites = []string{}
    }	

    // Handle adding or removing favourite
    path := r.URL.Path
    if path == "/add_favourite" {
        if !contains(clientData.Favourites, directory) {
            clientData.Favourites = append(clientData.Favourites, directory)
        }
    } else if path == "/remove_favourite" {
        clientData.Favourites = remove(clientData.Favourites, directory)
    } else {
        http.Error(w, "Invalid endpoint", http.StatusBadRequest)
        return
    }

    // Write updated data back to the file
    if err := writeData(clientID, "favourites", clientData); err != nil {
        log.Printf("writeData failed for favourites %s: %v\n", clientID, err)
        http.Error(w, "Failed to save favourites", http.StatusInternalServerError)
        return
    }

    // Respond with success
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"result": "ok"})
}

// Helper function to check if a slice contains a specific item
func contains(slice []string, item string) bool {
    for _, a := range slice {
        if a == item {
            return true
        }
    }
    return false
}

// Helper function to remove an item from a slice
func remove(slice []string, item string) []string {
    for i, a := range slice {
        if a == item {
            return append(slice[:i], slice[i+1:]...)
        }
    }
    return slice
}

func activePlayersHandler(w http.ResponseWriter, r *http.Request) {
	// Implement active players handler
}


func radioHistoryHandler(w http.ResponseWriter, r *http.Request) {
    clientID := r.URL.Query().Get("client_id")
    if clientID == "" {
        http.Error(w, "client_id is required", http.StatusBadRequest)
        return
    }

    clientData, err := readData(clientID, "radio_history")
    if err != nil {
        log.Printf("readData failed for radio_history %s: %v\n", clientID, err)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"tree": []interface{}{},
			"info": map[string]interface{}{},
			})
		return
    }

    // Return the clientData in JSON format
    w.Header().Set("Content-Type", "application/json")
    if err := json.NewEncoder(w).Encode(clientData); err != nil {
        log.Printf("Failed to encode radio history data for %s: %v\n", clientID, err)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"tree": []interface{}{},
			"info": map[string]interface{}{},
			})
		return
    }
}

func bandcampHistoryHandler(w http.ResponseWriter, r *http.Request) {
    clientID := r.URL.Query().Get("client_id")
    if clientID == "" {
        http.Error(w, "client_id is required", http.StatusBadRequest)
        return
    }

    clientData, err := readData(clientID, "bandcamp_history")
    w.Header().Set("Content-Type", "application/json")

	if err != nil {
        log.Printf("readData failed for bandcamp_history %s: %v\n", clientID, err)
        // http.Error(w, "Failed to load bandcamp history", http.StatusInternalServerError)
        // return
		json.NewEncoder(w).Encode(map[string]interface{}{
			"tree": []interface{}{},
			"info": map[string]interface{}{},
			})
		return
    }

    // Return the clientData in JSON format
    
    if err := json.NewEncoder(w).Encode(clientData); err != nil {
        log.Printf("Failed to encode bandcamp history data for %s: %v\n", clientID, err)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"tree": []interface{}{},
			"info": map[string]interface{}{},
			})
		// http.Error(w, "Failed to encode data", http.StatusInternalServerError)
        return
    }
}

func isHTTP(s string) bool {
    return strings.HasPrefix(s, "http:")
}

func formatPlaytime(playtime int) string {
    hours := playtime / 3600
    minutes := (playtime % 3600) / 60
    seconds := playtime % 60
    return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
}

func capitalizeFirstLetter(s string) string {
	if len(s) == 0 {
		return s
	}
	// Capitalize the first letter and append the rest of the string
	return strings.ToUpper(string(s[0])) + s[1:]
}

func dataHandler(w http.ResponseWriter, r *http.Request) {
	clientDataTree := map[string]interface{}{
		"tree": []interface{}{},
		"info": map[string]interface{}{},
	}

	clientID := r.URL.Query().Get("client_id")
	dataPath := r.URL.Path[1:]

	log.Printf("%v - %v", clientID, dataPath)

	// Read the data based on the path (history, randomset, etc.)
	ClientData, err := readData(clientID, dataPath)
	if err != nil {
		log.Printf("readData failed %s: %v\n", clientID, err)
	}

	// Function to process the data for any string slice (History, Randomset, etc.)
	processDirectories := func(directories []string) []FileInfo {
		var fileInfos []FileInfo

		for _, directory := range directories {
			log.Printf("processing %v", directory)
			directoryStr := directory

			// If it's not a root or HTTP directory
			if directoryStr != "/" && !isHTTP(directoryStr) {
				count, err := mpdClient.Client().Count("base", directoryStr)
				if err != nil {
					log.Printf("Could not get count for %s: %v\n", directoryStr, err)
					continue
				}
				seconds, err := strconv.Atoi(count[1])

				fileInfo := FileInfo{
					Directory: directoryStr,
					Count: map[string]interface{}{
						"playhours": formatPlaytime(seconds),
						"playtime":  count[1],
						"songs":     count[0],
					},
				}
				fileInfos = append(fileInfos, fileInfo)
			} else if isHTTP(directoryStr) {
				// Handle HTTP directory
				fileInfo := FileInfo{
					Directory: directoryStr,
					Stream:    directoryStr,
				}
				fileInfos = append(fileInfos, fileInfo)
			}
		}

		// Reverse the fileInfos slice
		for i, j := 0, len(fileInfos)-1; i < j; i, j = i+1, j-1 {
			fileInfos[i], fileInfos[j] = fileInfos[j], fileInfos[i]
		}

		return fileInfos
	}

	// Process different fields in ClientData
	var fileInfos []FileInfo

	// Check if History has data and process it
	if len(ClientData.History) > 0 {
		fileInfos = append(fileInfos, processDirectories(ClientData.History)...)
	}

	// Check if Randomset has data and process it
	if len(ClientData.Randomset) > 0 {
		fileInfos = append(fileInfos, processDirectories(ClientData.Randomset)...)
	}

	// Check if Favourites has data and process it
	if len(ClientData.Favourites) > 0 {
		fileInfos = append(fileInfos, processDirectories(ClientData.Favourites)...)
	}

	// Assign the processed fileInfos to the tree
	clientDataTree["tree"] = fileInfos

	// Write the response
	json.NewEncoder(w).Encode(clientDataTree)
}

func searchRadioHandler(w http.ResponseWriter, r *http.Request) {
	// Implement search radio handler
}

func searchBandcampHandler(w http.ResponseWriter, r *http.Request) {
	// Implement search bandcamp handler
}

func removeHistoryHandler(w http.ResponseWriter, r *http.Request) {
	// Implement remove history handler
}

func mpdProxyHandler(w http.ResponseWriter, r *http.Request) {
    var content map[string]interface{} = make(map[string]interface{})
    var err error
	log.Printf("mpdProxyHandler url: %v", r.URL.Path)
    switch r.URL.Path {
    case "/play":
        err = mpdClient.Client().Play(-1)
        content["message"] = "Playing"
    case "/pause":
        err = mpdClient.Client().Pause(true)
        content["message"] = "Paused"
    case "/playpause":
        status, err := mpdClient.Client().Status()
        if err != nil {
            http.Error(w, "Failed to get status", http.StatusInternalServerError)
            return
        }
        if status["state"] == "pause" {
            err = mpdClient.Client().Play(-1)
            content["message"] = "Playing"
        } else {
            err = mpdClient.Client().Pause(true)
            content["message"] = "Paused"
        }
    case "/next":
        err = mpdClient.Client().Next()
        content["message"] = "Next track"
    case "/prev":
        err = mpdClient.Client().Previous()
        content["message"] = "Previous track"
    case "/stop":
        err = mpdClient.Client().Stop()
        content["message"] = "Stopped"
    case "/status":
        status, err := mpdClient.Client().Status()
        if err != nil {
            http.Error(w, "Failed to get status", http.StatusInternalServerError)
            return
        }
        content["status"] = status
	case "/listfiles":
		directory := r.URL.Query().Get("directory")
		if directory == "" {
			directory = "."
		}
		files, err := mpdClient.Client().ListInfo(directory)
		if err != nil {
			http.Error(w, "Failed to list files", http.StatusInternalServerError)
			return
		}
		content["tree"] = files
	case "/lsinfo":
		directory := r.URL.Query().Get("directory")
		if directory == "" {
			directory = "."
		}
		info, err := mpdClient.Client().ListInfo(directory)
		if err != nil {
			http.Error(w, "Failed to get lsinfo", http.StatusInternalServerError)
			return
		}
		content["tree"] = info


	case "/search":
		pattern := r.URL.Query().Get("pattern")
		if pattern == "" {
			pattern = "ugar"
		}
		searchResult, err := mpdClient.Client().Search("any", pattern)
		if err != nil {
			http.Error(w, "Failed to search", http.StatusInternalServerError)
			return
		}
	
		var fileInfos []FileInfo
		for _, fileRecord := range searchResult {
			file := fileRecord["file"]
			fileInfo := FileInfo{
				LastModified: fileRecord["last-modified"],
				Artist:      fileRecord["artist"],
				Album:      fileRecord["album"],
				File:        file,
				Format:      fileRecord["format"],
				// Add additional fields here
				Duration:    fileRecord["duration"],
				Genre:       fileRecord["genre"],
				Codec:       fileRecord["codec"],
				Track:       fileRecord["track"],
				Date:        fileRecord["date"],
				Title:        fileRecord["title"],
			}
			fileInfos = append(fileInfos, fileInfo)
		}
	
		// Get directories from search results
		resultDirectories := make(map[string]bool)
		for _, elem := range searchResult {
			file := elem["file"]
			resultDirectories[filepath.Dir(file)] = true
		}
	
		// Add directories to fileInfos
		for directory := range resultDirectories {

			subDirCount, err := mpdClient.Client().Count("base", directory)
			log.Printf("subDirCount: %v", subDirCount)
			if err != nil {
				http.Error(w, "Failed to count", http.StatusInternalServerError)
				return
			}
			seconds, err := strconv.Atoi(subDirCount[1])
			if err != nil {
				http.Error(w, "Failed to parse count", http.StatusInternalServerError)
				return
			}
			duration := time.Duration(seconds) * time.Second
			hours := fmt.Sprintf("%d", int(duration.Hours()))
			minutes := fmt.Sprintf("%02d", int(duration.Minutes())%60)
			secondsStr := fmt.Sprintf("%02d", int(duration.Seconds())%60)
			playhours := fmt.Sprintf("%s:%s:%s", hours, minutes, secondsStr)
	
			fileInfo := FileInfo{
				Directory:    directory,
				Count: map[string]interface{}{
					"playhours": playhours,
					"playtime":  subDirCount[1],
					"songs":     subDirCount[0],
				},
			}
			fileInfos = append(fileInfos, fileInfo)
		}
	
		content["tree"] = fileInfos


	case "/ls":
		directory := r.URL.Query().Get("directory")
		if directory == "" {
			directory = "/"
		}
		if directory == "." {
			directory = "/"
		}
		listFiles, err := mpdClient.Client().ListInfo(directory)
		if err != nil {
			log.Printf("%v", err)
			http.Error(w, "Failed to list files", http.StatusInternalServerError)
			return
		}
	
		lsInfo := listFiles

		var count []string

		if directory != "/" {
			count, err = mpdClient.Client().Count("base", directory)
		} else {
			count, err = mpdClient.Client().Count("modified-since", "0")
		}
		log.Printf("count: %v", count)
	
		musicFiles := make(map[string]bool)
		for _, file := range lsInfo {
			file := file["file"]
			musicFiles[filepath.Base(file)] = true
		}
		for _, fileRecord := range listFiles {
			file := fileRecord["file"]
			if _, ok := musicFiles[filepath.Base(file)]; !ok {
				fileRecord["file"] = directory + "/" + file
				lsInfo = append(lsInfo, fileRecord)
			}
		}
		log.Printf("lsinfo g2: %v", lsInfo)
	
		// Adjusting the info structure
		info := map[string]string{
			"playtime": count[1],
			"songs":    count[0],
		}
		content["info"] = info
	
		// Adjusting the tree structure
		var fileInfos []FileInfo
		log.Printf("iterating lsinfo")
		for _, name := range lsInfo {
			log.Printf("name: %v", name)
			dir := name["directory"]
			if dir == "" {
				fileInfo := FileInfo{
					LastModified: name["last-modified"],
					Artist:      name["artist"],
					Album:      name["album"],
					File:        name["file"],
					Format:      name["format"],
					// Add additional fields here
					Duration:    name["duration"],
					Genre:       name["genre"],
					Codec:       name["codec"],
					Track:       name["track"],
					Date:        name["date"],
					Title:        name["title"],
				}
				fileInfos = append(fileInfos, fileInfo)
				continue
			}
			subDir := dir

			subDirCount, err := mpdClient.Client().Count("base", dir)
			log.Printf("subDirCount: %v", subDirCount)
			seconds, err := strconv.Atoi(subDirCount[1])
			if err != nil {
				http.Error(w, "Failed to parse count", http.StatusInternalServerError)
				return
			}
			duration := time.Duration(seconds) * time.Second
			hours := fmt.Sprintf("%d", int(duration.Hours()))
			minutes := fmt.Sprintf("%02d", int(duration.Minutes())%60)
			secondsStr := fmt.Sprintf("%02d", int(duration.Seconds())%60)
			playhours := fmt.Sprintf("%s:%s:%s", hours, minutes, secondsStr)
	
			fileInfo := FileInfo{
				Directory:    subDir,
				LastModified: name["last-modified"],
				Count: map[string]interface{}{
					"playhours": playhours,
					"playtime":  subDirCount[1],
					"songs":     subDirCount[0],
				},
				Artist:      name["artist"],
				Album:      name["album"],
				File:        name["file"],
				Format:      name["format"],
				// Add additional fields here
				Duration:    name["duration"],
				Genre:       name["genre"],
				Codec:       name["codec"],
				Track:       name["track"],
				Date:        name["date"],
				Title:        name["title"],
			}
			fileInfos = append(fileInfos, fileInfo)
		}
	
		log.Printf("fileInfos: %v", fileInfos)
		content["tree"] = fileInfos
	
	case "/addplay":
		// var playables []string
		playable := r.URL.Query().Get("directory")
		if playable == "" {
			playable = r.URL.Query().Get("url")
		}

		// Check if the playable URL is a radio station
		if r.URL.Query().Get("stationuuid") != "" && !strings.Contains(playable, "bandcamp.com") && !strings.Contains(playable, "youtube") && !strings.Contains(playable, "youtu.be") {
			// Placeholder for pyradios implementation
			stationURL := getRadioStationURL(r.URL.Query().Get("stationuuid"))
			playable = stationURL
		}
		
		// Check if the playable URL is a YouTube video
		if strings.Contains(playable, "youtube") || strings.Contains(playable, "youtu.be") || strings.Contains(playable, "bandcamp.com") {
			// Use yt-dlp command line to get the video URL
			cmd := exec.Command("yt-dlp", "-f", "bestaudio", "-g", playable)
			output, err := cmd.CombinedOutput()
			if err != nil {
				log.Println(err)
				return
			}
			playable = string(output)
		}
		
		log.Printf("%v", playable)
		
		mpdClient.Client().Consume(true)
		mpdClient.Client().Add(playable)
		mpdClient.Client().Play(0)
	
		// Manage history
		clientID := r.URL.Query().Get("client_id")
		if clientID != "" {
			ClientData, err := readData(clientID, "history")
			if err != nil {
				log.Println(err)
			}
			
			// Check if 'playable' exists in the client history
			for i, item := range ClientData.History {
				if item == playable {
					// Remove the item from history
					ClientData.History = append(ClientData.History[:i], ClientData.History[i+1:]...)
					break
				}
			}

			ClientData.History = append(ClientData.History, playable)

			// Limit the history to the last 10 items
			if len(ClientData.History) > 10 {
				ClientData.History = ClientData.History[len(ClientData.History)-10:]
			}

			ClientDataFile := filepath.Join(clientDB, fmt.Sprintf("%s.history.json", clientID))

			// Validate the client ID and file path
			re := regexp.MustCompile(`[^A-Za-z0-9_\-\.]`)
			if clientID != "" && filepath.HasPrefix(ClientDataFile, clientDB) && re.MatchString(ClientDataFile) {
				// Write the updated history back to the file
				file, err := os.Create(ClientDataFile)
				if err != nil {
					http.Error(w, "Unable to write file", http.StatusInternalServerError)
					return
				}
				defer file.Close()

				// Write JSON data to file
				encoder := json.NewEncoder(file)
				if err := encoder.Encode(ClientData); err != nil {
					http.Error(w, "Failed to write JSON data", http.StatusInternalServerError)
					return
				}
			}			


		}


	
	default:
        http.Error(w, "Not Found", http.StatusNotFound)
        return
    }

    if err != nil {
        http.Error(w, fmt.Sprintf("Failed to execute command: %v", err), http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(content)
}

func getActivePlayers() []map[string]interface{} {
	players := []map[string]interface{}{}

	// Initialize Redis client
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   0,
	})

	defer rdb.Close()

	// Scan for keys
	iter := rdb.Scan(ctx, 0, "upnp:player:*:last_seen", 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		lastSeenStr, err := rdb.Get(ctx, key).Result()
		if err != nil {
			log.Printf("Failed to get last seen: %v", err)
			continue
		}

		lastSeen, err := strconv.ParseFloat(lastSeenStr, 64)
		if err != nil {
			log.Printf("Failed to parse last seen: %v", err)
			continue
		}

		if time.Now().Unix()-int64(lastSeen) < 1200 {
			dataStr, err := rdb.Get(ctx, strings.Replace(key, "last_seen", "data", 1)).Result()
			if err != nil {
				log.Printf("Failed to get data: %v", err)
				continue
			}

			var data map[string]interface{}
			if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
				log.Printf("Failed to unmarshal data: %v", err)
				continue
			}

			players = append(players, data)
		}

		// Housekeeping
		if time.Now().Unix()-int64(lastSeen) > 1800 {
			rdb.Del(ctx, strings.Replace(key, "last_seen", "data", 1))
			rdb.Del(ctx, key)
		}
	}
	if err := iter.Err(); err != nil {
		log.Printf("Failed to iterate: %v", err)
	}

	return players
}



func processCurrentSong(currentsong map[string]interface{}) map[string]interface{} {
	// Initialize states
	states := []string{"play", "pause", "stop"}
	for _, state := range states {
		currentsong[state] = false
		if currentsong["state"] == state {
			currentsong[state] = true
		}
	}

	// Set title and display titles

	if currentsong["Title"] == "" {
		if file, ok := currentsong["file"]; ok {
			currentsong["title"] = file
			currentsong["display_title"] = file
			currentsong["display_title_top"] = ""
		}
	} else {
		titleElements := []string{}
		if track, ok := currentsong["Track"].(string); ok {
			titleElements = append(titleElements, track)
		}
		if title, ok := currentsong["Title"].(string); ok {
			titleElements = append(titleElements, title)
		}

		albumElements := []string{}
		if artist, ok := currentsong["Artist"].(string); ok {
			albumElements = append(albumElements, artist)
		}
		if album, ok := currentsong["Album"].(string); ok {
			albumElements = append(albumElements, album)
		}

		currentsong["display_title"] = strings.Join(titleElements, " - ")
		currentsong["display_title_top"] = strings.Join(albumElements, " - ")
	}

	// Set active state
	currentsong["active"] = false
	if state, ok := currentsong["state"].(string); ok {
		if state == "play" || state == "pause" {
			currentsong["active"] = true
		}
	}

	// Set not playing state
	if !currentsong["active"].(bool) {
		currentsong["title"] = "not playing"
		currentsong["display_title"] = "not playing"
	}

	// Set next state and title
	if state, ok := currentsong["state"].(string); ok {
		if state == "play" {
			currentsong["next_state"] = "pause"
			currentsong["next_title"] = "playing ➙ pause"
			currentsong["next_icon"] = "pause_circle_outline"
		} else if state == "pause" {
			currentsong["next_state"] = "play"
			currentsong["next_title"] = "paused ➙ play"
			currentsong["next_icon"] = "play_circle_outline"
		}
	}

	return currentsong
}


func currentSongHandler(w http.ResponseWriter, r *http.Request) {
	// Connect to MPD


	// Get current song and status
	currentsong, err := mpdClient.Client().CurrentSong()
	if err != nil {
		http.Error(w, "Failed to get current song", http.StatusInternalServerError)
		log.Printf("Failed to get current song: %v", err)
		return
	}

	status, err := mpdClient.Client().Status()
	if err != nil {
		http.Error(w, "Failed to get status", http.StatusInternalServerError)
		log.Printf("Failed to get status: %v", err)
		return
	}

	// Convert mpd.Attrs to map[string]interface{}
	currentsongMap := make(map[string]interface{})
	for k, v := range currentsong {
		currentsongMap[k] = v
	}
	for k, v := range status {
		currentsongMap[k] = v
	}

	// Add additional information
	currentsongMap["players"] = getActivePlayers()
	currentsongMap["bandcamp_enabled"] = bandcamp_enabled
	currentsongMap["default_stream"] = "http://" + os.Getenv("hostname") + ":8000/audio.ogg"

	// Process current song
	currentsongMap = processCurrentSong(currentsongMap)

	// Convert map[string]interface{} back to mpd.Attrs
	processedSong := make(mpd.Attrs)
	for k, v := range currentsongMap {
		processedSong[k] = fmt.Sprintf("%v", v)
	}

	// Return the content as JSON
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(processedSong)
}

func countHandler(w http.ResponseWriter, r *http.Request) {
	// Get the directory from the request
	directory := r.URL.Query().Get("directory")
	if directory == "" {
		directory = "."
	}
	log.Printf("Directory: %v", directory)


	// Get the count of the specified directory

	count, err := mpdClient.Client().Count("base", directory)

	if err != nil {
		http.Error(w, "Failed to get count", http.StatusInternalServerError)
		log.Printf("Failed to get count: %v", err)
		return
	}
	log.Printf("Count: %v", count)
	// Calculate the play hours
	seconds, err := strconv.Atoi(count[1])
	duration := time.Duration(seconds) * time.Second
	hours := int(duration.Hours())
	minutes := int(duration.Minutes()) % 60

	// Prepare the content to return
	content := map[string]interface{}{
		"playhours": fmt.Sprintf("%d:%02d", hours, minutes),
		"count": count[0],
		"name":      directory,
	}


	// Return the content as JSON
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(content)
}


func toggleOutputHandler(w http.ResponseWriter, r *http.Request) {
	outputID := r.URL.Query().Get("output")
	log.Printf("outputID: %v", outputID)


	// Get the current outputs
	outputs, err := mpdClient.Client().ListOutputs()
	log.Printf("outputs: %v", outputs)

	if err != nil {
		http.Error(w, "Failed to get outputs", http.StatusInternalServerError)
		log.Printf("Failed to get outputs: %v", err)
		return
	}

	// Find the specified output and toggle its state
	for _, output := range outputs {
		if output["outputid"] == outputID {
			outputInt, err := strconv.Atoi(outputID)
			if output["outputenabled"] == "1" {
				err = mpdClient.Client().DisableOutput(outputInt)
			} else {
				err = mpdClient.Client().EnableOutput(outputInt)
			}
			if err != nil {
				http.Error(w, "Failed to toggle output", http.StatusInternalServerError)
				log.Printf("Failed to toggle output: %v", err)
				return
			}
			break
		}
	}

	// Get the updated outputs
	outputs, err = mpdClient.Client().ListOutputs()
	if err != nil {
		http.Error(w, "Failed to get outputs", http.StatusInternalServerError)
		log.Printf("Failed to get outputs: %v", err)
		return
	}

	// Return the outputs as JSON
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(outputs)
}


func catchAllHandler(w http.ResponseWriter, r *http.Request) {
    // Get the absolute path of the requested URL
    absPath := filepath.Join("./", r.URL.Path)

    // Check if the path is a file
    fileInfo, err := os.Stat(absPath)
    if err != nil || !fileInfo.Mode().IsRegular() {
        // If the path is not a file and does not end with a trailing slash, redirect to the path with a trailing slash
        if r.URL.Path[len(r.URL.Path)-1] != '/' {
            http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
            return
        }

        // Serve the index.html template
        tmpl, err := template.ParseFiles("index.html")
        if err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        tmpl.Execute(w, r.URL.Query())
    } else {
        // If the path is a file, you can serve it directly or handle it as needed
        http.ServeFile(w, r, absPath)
    }
}
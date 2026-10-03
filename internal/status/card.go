package status

import (
	"fmt"
	"strings"
)

// HostState is the mirror progress of one file host.
type HostState struct {
	Status string // pending|uploading|done|skipped|failed
	Pct    float64
	Speed  float64
	URL    string
	Error  string
}

// SystemStats is a host resource snapshot.
type SystemStats struct {
	CPU, RAM         float64
	DiskFree         uint64
	Uptime           float64
	NetSent, NetRecv uint64
}

// Snapshot is a point-in-time copy of job state for rendering.
type Snapshot struct {
	JobID, Status, Target, Args string
	SplitEnabled                bool

	// Download stage.
	Engine         string
	DownloadDone   bool
	Determinate    bool // true when Pct is meaningful
	Pct            float64
	Downloaded     int64
	Expected       int64
	Speed          float64
	CurrentFile    string
	FileCount      int
	TorrentName    string
	Seeders, Peers int
	IsTorrent      bool
	IsPatch        bool
	PatchStage     string
	PatchInput     string
	PatchOutput    string

	// Post-processing.
	Converting   bool
	ConvertFile  string
	Archiving    bool
	ArchiveFmt   string

	// Upload stage.
	TotalFiles   int
	Sent         int
	Skipped      int
	UploadFile   string
	UploadPct    float64
	UploadSpeed  float64
	UploadActive bool

	Hosts      map[string]HostState
	HostOrder  []string
	MirrorLabels map[string]string

	Stats *SystemStats
}

const bullet = "• "

func kv(b *strings.Builder, k, v string) { fmt.Fprintf(b, "%s<b>%s</b>: %s\n", bullet, k, v) }

// Job renders the live status card as Telegram HTML.
func Job(s Snapshot) string {
	var b strings.Builder
	if s.IsPatch {
		fmt.Fprintf(&b, "<b>APK Patching Pipeline</b> <code>#%s</code>\n\n<blockquote>", Esc(s.JobID))
		kv(&b, "Input", Code(s.PatchInput))
		kv(&b, "Output", Code(s.PatchOutput))
		kv(&b, "Signer", Code("JKS Keystore"))
		b.WriteString("</blockquote>\n")
		if !s.DownloadDone {
			fmt.Fprintf(&b, "<b>Pipeline</b>\n<blockquote>")
			stage := s.PatchStage
			if stage == "" {
				stage = "Initializing patch pipeline..."
			}
			kv(&b, "Stage", Code(stage))
			kv(&b, "State", Code("["+Marquee()+"]"))
			kv(&b, "Processed", Code(Size(float64(s.Downloaded))))
			b.WriteString("</blockquote>")
		} else {
			b.WriteString("<b>Uploader</b>\n<blockquote>")
			kv(&b, "Status", Code("Uploading patched APK to Telegram... ["+Marquee()+"]"))
			b.WriteString("</blockquote>")
		}
		return b.String()
	}

	fmt.Fprintf(&b, "<b>Task #%s</b>\n\n<blockquote>", Esc(s.JobID))
	kv(&b, "Status", Code(strings.ToUpper(s.Status)))
	if s.IsTorrent && s.TorrentName != "" {
		kv(&b, "Torrent", Code(Short(s.TorrentName, 60)))
	} else if s.Target != "" {
		kv(&b, "Target", Code(Short(s.Target, 60)))
	}
	if s.Args != "" {
		kv(&b, "Args", Code(Short(s.Args, 60)))
	}
	split := "Disabled"
	if s.SplitEnabled {
		split = "Enabled (2GB)"
	}
	kv(&b, "Auto Split", Code(split))
	b.WriteString("</blockquote>\n")

	if !s.DownloadDone {
		b.WriteString("<b>Downloader</b>\n<blockquote>")
		kv(&b, "Engine", Code(s.Engine))
		switch {
		case s.Determinate:
			kv(&b, "Progress", Code(fmt.Sprintf("%.1f%%", s.Pct))+" "+Code(Bar(s.Pct)))
			total := "Unknown"
			if s.Expected > 0 {
				total = Size(float64(s.Expected))
			}
			kv(&b, "Downloaded", Code(Size(float64(s.Downloaded))+" / "+total))
		default:
			kv(&b, "State", Code("["+Marquee()+"]"))
			kv(&b, "Downloaded", Code(Size(float64(s.Downloaded))))
			if s.FileCount > 0 {
				kv(&b, "Files", Code(fmt.Sprint(s.FileCount)))
			}
		}
		kv(&b, "Speed", Code(Size(s.Speed)+"/s"))
		if s.IsTorrent {
			kv(&b, "Swarm", Code(fmt.Sprintf("Seeders: %d | Leechers: %d", s.Seeders, s.Peers)))
		}
		if s.CurrentFile != "" {
			kv(&b, "Current", Code(Short(s.CurrentFile, 50)))
		}
		b.WriteString("</blockquote>\n")
	}

	if s.Converting {
		b.WriteString("<b>Converter</b>\n<blockquote>")
		kv(&b, "File", Code(Short(s.ConvertFile, 50)))
		kv(&b, "State", Code("["+Marquee()+"]"))
		b.WriteString("</blockquote>\n")
	}
	if s.Archiving {
		b.WriteString("<b>Archiver</b>\n<blockquote>")
		kv(&b, "Format", Code(strings.ToUpper(s.ArchiveFmt)))
		kv(&b, "State", Code("["+Marquee()+"]"))
		b.WriteString("</blockquote>\n")
	}

	if len(s.Hosts) > 0 {
		b.WriteString("<b>Mirror</b>\n<blockquote>")
		for i, key := range s.HostOrder {
			tree := "├"
			if i == len(s.HostOrder)-1 {
				tree = "└"
			}
			label := s.MirrorLabels[key]
			h := s.Hosts[key]
			switch h.Status {
			case "done":
				if h.URL != "" {
					fmt.Fprintf(&b, "%s <b><a href=\"%s\">%s</a></b>: %s\n", tree, Esc(h.URL), Esc(label), Code(h.URL))
				} else {
					fmt.Fprintf(&b, "%s <b>%s</b>: %s\n", tree, Esc(label), Code("Uploaded"))
				}
			case "uploading":
				spd := "0 B/s"
				if h.Speed > 0 {
					spd = Size(h.Speed) + "/s"
				}
				fmt.Fprintf(&b, "%s <b>%s</b>: %s <b>%.1f%%</b> (%s)\n", tree, Esc(label), Code(Bar(h.Pct)), h.Pct, spd)
			case "skipped":
				fmt.Fprintf(&b, "%s <b>%s</b>: %s\n", tree, Esc(label), Code("Skipped (>10GB)"))
			case "failed":
				fmt.Fprintf(&b, "%s <b>%s</b>: %s\n", tree, Esc(label), Code(Short(h.Error, 40)))
			default:
				fmt.Fprintf(&b, "%s <b>%s</b>: %s\n", tree, Esc(label), Code("Pending"))
			}
		}
		b.WriteString("</blockquote>\n")
	}

	if s.TotalFiles > 0 || s.Sent > 0 || s.UploadActive {
		b.WriteString("<b>Uploader</b>\n<blockquote>")
		kv(&b, "Files", Code(fmt.Sprintf("%d sent / %d total", s.Sent, s.TotalFiles)))
		if s.Skipped > 0 {
			kv(&b, "Skipped", Code(fmt.Sprint(s.Skipped)))
		}
		if s.UploadActive && s.UploadFile != "" {
			kv(&b, "Current", Code(Short(s.UploadFile, 50)))
			kv(&b, "Progress", Code(fmt.Sprintf("%.1f%%", s.UploadPct))+" "+Code(Bar(s.UploadPct)))
			kv(&b, "Speed", Code(Size(s.UploadSpeed)+"/s"))
		}
		b.WriteString("</blockquote>\n")
	}

	if s.Stats != nil {
		st := s.Stats
		b.WriteString("<b>System</b>\n<blockquote>")
		kv(&b, "CPU / RAM", Code(fmt.Sprintf("%.1f%% / %.1f%%", st.CPU, st.RAM)))
		kv(&b, "Disk Free", Code(Size(float64(st.DiskFree))))
		kv(&b, "Network", Code(fmt.Sprintf("↑%s ↓%s", Size(float64(st.NetSent)), Size(float64(st.NetRecv)))))
		kv(&b, "Uptime", Code(Duration(st.Uptime)))
		b.WriteString("</blockquote>")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Queued renders the card shown while a job waits in the queue.
func Queued(jobID, target, args string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Task #%s Queued</b>\n\n<blockquote>", Esc(jobID))
	kv(&b, "Status", Code("QUEUED"))
	kv(&b, "Target", Code(Short(target, 60)))
	if args != "" {
		kv(&b, "Args", Code(Short(args, 60)))
	}
	b.WriteString("</blockquote>")
	return b.String()
}

// Prompt and progress messages used by the interactive flows.

func ArchivePrompt(jobID, file string) string {
	return fmt.Sprintf("<b>Archive detected</b> <code>#%s</code>\n\n<blockquote>%s%s</blockquote>\nUpload the archive as-is, or extract it and upload the contents too? Defaults to <b>archive only</b> in 15 seconds.",
		Esc(jobID), bullet, Code(file))
}
func ArchiveChoice(jobID, file, choice string) string {
	return fmt.Sprintf("<b>Archive choice</b> <code>#%s</code>\n<blockquote>%s%s → <b>%s</b></blockquote>", Esc(jobID), bullet, Code(file), Esc(choice))
}
func AudioPrompt(jobID, file string) string {
	return fmt.Sprintf("<b>Audio conversion</b> <code>#%s</code>\n\n<blockquote>%s%s</blockquote>\nConvert to MP3 or upload the original? Defaults to <b>original</b> in 15 seconds.",
		Esc(jobID), bullet, Code(file))
}
func ConversionChoice(jobID, file, choice string) string {
	return fmt.Sprintf("<b>Conversion choice</b> <code>#%s</code>\n<blockquote>%s%s → <b>%s</b></blockquote>", Esc(jobID), bullet, Code(file), Esc(choice))
}
func Extracting(jobID, file string) string {
	return fmt.Sprintf("<b>Extracting</b> <code>#%s</code>\n<blockquote>%s%s\n%s%s</blockquote>", Esc(jobID), bullet, Code(file), bullet, Code("["+Marquee()+"]"))
}
func ExtractionOK(jobID, file string) string {
	return fmt.Sprintf("<b>Extraction complete</b> <code>#%s</code>\n<blockquote>%s%s</blockquote>", Esc(jobID), bullet, Code(file))
}
func ExtractionFailed(jobID, file string) string {
	return fmt.Sprintf("<b>Extraction failed</b> <code>#%s</code>\n<blockquote>%s%s</blockquote>", Esc(jobID), bullet, Code(file))
}
func Converting(jobID, file, kind string) string {
	return fmt.Sprintf("<b>Converting to %s</b> <code>#%s</code>\n<blockquote>%s%s\n%s%s</blockquote>", Esc(kind), Esc(jobID), bullet, Code(file), bullet, Code("["+Marquee()+"]"))
}
func ConversionFailed(jobID, file string) string {
	return fmt.Sprintf("<b>Conversion failed</b> <code>#%s</code>\n<blockquote>%s%s (uploading original)</blockquote>", Esc(jobID), bullet, Code(file))
}

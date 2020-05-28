package main

import (
	"bytes"
	"encoding/gob"
	"flag"
	"github.com/boltdb/bolt"
	"github.com/esrrhs/go-engine/src/common"
	"github.com/esrrhs/go-engine/src/loggo"
	"github.com/esrrhs/go-engine/src/threadpool"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func main() {

	defer common.CrashLog()

	src := flag.String("src", "", "src image path")
	target := flag.String("target", "", "target image path")
	lib := flag.String("lib", "", "lib image path")
	worker := flag.Int("worker", 10, "worker thread num")
	database := flag.String("database", "./database.bin", "cache datbase")

	flag.Parse()

	if *src == "" || *target == "" || *lib == "" {
		flag.Usage()
		return
	}

	level := loggo.LEVEL_INFO
	loggo.Ini(loggo.Config{
		Level:  level,
		Prefix: "mosaic",
		MaxDay: 3,
	})
	loggo.Info("start...")

	loggo.Info("src %s", *src)
	loggo.Info("target %s", *target)
	loggo.Info("lib %s", *lib)

	parse_src(*src)
	load_lib(*lib, *worker, *database)
	gen_target(*target)
}

func parse_src(src string) {
	loggo.Info("parse_src %s", src)

}

func gen_target(target string) {
	loggo.Info("gen_target %s", target)

}

type FileInfo struct {
	Filename string
	R        uint8
	G        uint8
	B        uint8
}

type CalFileInfo struct {
	fi   FileInfo
	ok   bool
	done bool
}

type ImageData struct {
	filename []string
	index    int
	r        uint8
	g        uint8
	b        uint8
}

var gcolordata []ImageData

func load_lib(lib string, workernum int, database string) {
	loggo.Info("load_lib %s", lib)

	loggo.Info("load_lib start ini database")
	for i := 0; i <= 255; i++ {
		for j := 0; j <= 255; j++ {
			for z := 0; z <= 255; z++ {
				gcolordata = append(gcolordata, ImageData{})
			}
		}
	}

	for i := 0; i <= 255; i++ {
		for j := 0; j <= 255; j++ {
			for z := 0; z <= 255; z++ {
				k := make_key(uint8(i), uint8(j), uint8(z))
				gcolordata[k].r, gcolordata[k].g, gcolordata[k].b = uint8(i), uint8(j), uint8(z)
			}
		}
	}

	loggo.Info("load_lib ini database ok")

	loggo.Info("load_lib start load database")

	db, err := bolt.Open(database, 0600, nil)
	if err != nil {
		loggo.Error("load_lib Open database fail %s %s", database, err)
	}
	defer db.Close()

	db.Update(func(tx *bolt.Tx) error {
		tx.CreateBucketIfNotExists([]byte("FileInfo"))
		b := tx.Bucket([]byte("FileInfo"))

		need_del := make([]string, 0)

		b.ForEach(func(k, v []byte) error {

			var b bytes.Buffer
			b.Write(v)

			dec := gob.NewDecoder(&b)
			var fi FileInfo
			err = dec.Decode(&fi)
			if err != nil {
				loggo.Error("load_lib Open database Decode fail %s %s %s", database, string(k), err)
				need_del = append(need_del, string(k))
				return nil
			}

			if _, err := os.Stat(fi.Filename); os.IsNotExist(err) {
				loggo.Error("load_lib Open Filename IsNotExist, need delete %s %s %s", database, fi.Filename, err)
				need_del = append(need_del, string(k))
				return nil
			}

			return nil
		})

		for _, k := range need_del {
			b.Delete([]byte(k))
		}

		return nil
	})

	loggo.Info("load_lib load database ok")

	loggo.Info("load_lib start get image file list")
	imagefilelist := make([]CalFileInfo, 0)
	cached := 0
	filepath.Walk(lib, func(path string, f os.FileInfo, err error) error {

		if f == nil || f.IsDir() {
			return nil
		}

		if !strings.HasSuffix(strings.ToLower(f.Name()), ".jpeg") &&
			!strings.HasSuffix(strings.ToLower(f.Name()), ".jpg") &&
			!strings.HasSuffix(strings.ToLower(f.Name()), ".png") &&
			!strings.HasSuffix(strings.ToLower(f.Name()), ".gif") {
			return nil
		}

		abspath, err := filepath.Abs(path)
		if err != nil {
			loggo.Error("load_lib get Abs fail %s %s %s", database, path, err)
			return nil
		}

		db.View(func(tx *bolt.Tx) error {
			b := tx.Bucket([]byte("FileInfo"))
			v := b.Get([]byte(abspath))
			if v == nil {
				imagefilelist = append(imagefilelist, CalFileInfo{fi: FileInfo{abspath, 0, 0, 0}})
			} else {
				cached++
			}
			return nil
		})

		return nil
	})

	loggo.Info("load_lib get image file list ok %d cache %d", len(imagefilelist), cached)

	loggo.Info("load_lib start calc image avg color %d", len(imagefilelist))
	var worker int32
	begin := time.Now()
	last := time.Now()
	var done int32
	var donesize int64

	atomic.AddInt32(&worker, 1)
	var save_inter int
	go save_to_database(&worker, &imagefilelist, db, &save_inter)

	tp := threadpool.NewThreadPool(workernum, 16, func(in interface{}) {
		i := in.(int)
		calc_avg_color(&imagefilelist[i], &worker, &done, &donesize)
	})

	i := 0
	for worker != 0 {
		if i < len(imagefilelist) {
			ret := tp.AddJobTimeout(int(common.RandInt()), i, 10)
			if ret {
				atomic.AddInt32(&worker, 1)
				i++
			}
		} else {
			time.Sleep(time.Millisecond * 10)
		}
		if time.Now().Sub(last) >= time.Second {
			last = time.Now()
			speed := int(done) / (int(time.Now().Sub(begin)) / int(time.Second))
			left := ""
			if speed > 0 {
				left = time.Duration(int64((len(imagefilelist)-int(done))/speed) * int64(time.Second)).String()
			}
			donesizem := donesize / 1024 / 1024
			dataspeed := int(donesizem) / (int(time.Now().Sub(begin)) / int(time.Second))
			loggo.Info("speed=%d/s percent=%d%% time=%s thead=%d progress=%d/%d saved=%d data=%dM dataspeed=%dM/s", speed, int(done)*100/len(imagefilelist),
				left, int(worker), int(done), len(imagefilelist), save_inter, donesizem, dataspeed)
		}
	}

	loggo.Info("load_lib calc image avg color ok %d %d", len(imagefilelist), done)

	loggo.Info("load_lib start save image avg color")

	maxcolornum := 0
	totalnum := 0
	db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("FileInfo"))

		b.ForEach(func(k, v []byte) error {

			var b bytes.Buffer
			b.Write(v)

			dec := gob.NewDecoder(&b)
			var fi FileInfo
			err = dec.Decode(&fi)
			if err != nil {
				loggo.Error("load_lib Open database Decode fail %s %s %s", database, string(k), err)
				return nil
			}

			key := make_key(fi.R, fi.G, fi.B)
			gcolordata[key].filename = append(gcolordata[key].filename, fi.Filename)
			if len(gcolordata[key].filename) > maxcolornum {
				maxcolornum = len(gcolordata[key].filename)
			}
			totalnum++

			return nil
		})

		return nil
	})

	loggo.Info("load_lib save image avg color ok total %d max %d", totalnum, maxcolornum)

	tmpcolornum := make(map[int]int)
	tmpcolorone := make(map[int]ImageData)
	colorgourp := []struct {
		name string
		c    color.RGBA
		num  int
	}{
		{"Black 	", common.Black, 0},
		{"White 	", common.White, 0},
		{"Red 	", common.Red, 0},
		{"Lime 	", common.Lime, 0},
		{"Blue 	", common.Blue, 0},
		{"Yellow ", common.Yellow, 0},
		{"Cyan	", common.Cyan, 0},
		{"Magenta", common.Magenta, 0},
		{"Silver ", common.Silver, 0},
		{"Gray 	", common.Gray, 0},
		{"Maroon ", common.Maroon, 0},
		{"Olive 	", common.Olive, 0},
		{"Green 	", common.Green, 0},
		{"Purple	", common.Purple, 0},
		{"Teal 	", common.Teal, 0},
		{"Navy 	", common.Navy, 0},
	}

	for _, data := range gcolordata {
		tmpcolornum[len(data.filename)]++
		tmpcolorone[len(data.filename)] = data

		if len(data.filename) > 0 {
			min := 0
			mindistance := math.MaxFloat64
			for index, cg := range colorgourp {
				diff := common.ColorDistance(color.RGBA{data.r, data.g, data.b, 0}, cg.c)
				if diff < mindistance {
					min = index
					mindistance = diff
				}
			}

			colorgourp[min].num++
		}
	}

	for i := 0; i <= maxcolornum; i++ {
		str := ""
		if tmpcolornum[i] == 1 {
			str = make_string(tmpcolorone[i].r, tmpcolorone[i].g, tmpcolorone[i].b)
		}
		loggo.Info("load_lib avg color num distribution %d = %d %s", i, tmpcolornum[i], str)
	}

	for _, cg := range colorgourp {
		loggo.Info("load_lib avg color color distribution %s = %d", cg.name, cg.num)
	}
}

func make_key(r uint8, g uint8, b uint8) int {
	return int(r)*256*256 + int(g)*256 + int(b)
}

func make_string(r uint8, g uint8, b uint8) string {
	return "r " + strconv.Itoa(int(r)) + " g " + strconv.Itoa(int(g)) + " b " + strconv.Itoa(int(b))
}

func calc_avg_color(cfi *CalFileInfo, worker *int32, done *int32, donesize *int64) {
	defer common.CrashLog()
	defer atomic.AddInt32(worker, -1)
	defer atomic.AddInt32(done, 1)
	defer func() {
		cfi.done = true
	}()

	reader, err := os.Open(cfi.fi.Filename)
	if err != nil {
		loggo.Error("calc_avg_color Open fail %s %s", cfi.fi.Filename, err)
		return
	}
	defer reader.Close()

	fi, err := reader.Stat()
	if err != nil {
		loggo.Error("calc_avg_color Stat fail %s %s", cfi.fi.Filename, err)
		return
	}
	filesize := fi.Size()
	defer atomic.AddInt64(donesize, filesize)

	img, _, err := image.Decode(reader)
	if err != nil {
		loggo.Error("calc_avg_color Decode image fail %s %s", cfi.fi.Filename, err)
		return
	}

	bounds := img.Bounds()

	len := common.MinOfInt(bounds.Dx(), bounds.Dy())
	startx := bounds.Min.X + (bounds.Dx()-len)/2
	starty := bounds.Min.Y + (bounds.Dy()-len)/2
	endx := common.MinOfInt(startx+len, bounds.Max.X)
	endy := common.MinOfInt(starty+len, bounds.Max.Y)

	var sumR, sumG, sumB, count float64

	for y := starty; y < endy; y++ {
		for x := startx; x < endx; x++ {
			r, g, b, _ := img.At(x, y).RGBA()

			sumR += float64(r)
			sumG += float64(g)
			sumB += float64(b)

			count += 1
		}
	}

	cfi.fi.R = uint8(sumR / count)
	cfi.fi.G = uint8(sumG / count)
	cfi.fi.B = uint8(sumB / count)
	cfi.ok = true

	return
}

func save_to_database(worker *int32, imagefilelist *[]CalFileInfo, db *bolt.DB, save_inter *int) {
	defer common.CrashLog()
	defer atomic.AddInt32(worker, -1)

	i := 0
	for {
		if i >= len(*imagefilelist) {
			return
		}

		cfi := (*imagefilelist)[i]
		if cfi.done {
			i++

			if cfi.ok {
				var b bytes.Buffer

				enc := gob.NewEncoder(&b)
				err := enc.Encode(&cfi.fi)
				if err != nil {
					loggo.Error("calc_avg_color Encode FileInfo fail %s %s", cfi.fi.Filename, err)
					return
				}

				k := []byte(cfi.fi.Filename)
				v := b.Bytes()

				db.Update(func(tx *bolt.Tx) error {
					b := tx.Bucket([]byte("FileInfo"))
					err := b.Put(k, v)
					return err
				})
			}

			*save_inter = i
		} else {
			time.Sleep(time.Millisecond * 10)
		}
	}
}

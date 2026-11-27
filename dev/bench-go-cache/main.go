// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

type phaseTiming struct {
	fetchAndLoad  time.Duration
	compile       time.Duration
	linkAndRun    time.Duration
	total         time.Duration
	downloads     int
	compiledCount int
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bench-go-cache:", err)
		os.Exit(1)
	}
}

func run() error {
	iterations := flag.Int("runs", 5, "number of iterations to take the median of")
	firstTarget := flag.String("first", "helm.sh/helm/v3/cmd/helm@v3.19.0", "first go package@version")
	firstArgsFlag := flag.String("first-args", "version --short", "space-separated args for first script")
	secondTarget := flag.String("second", "sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0", "second go package@version")
	secondArgsFlag := flag.String("second-args", "--version", "space-separated args for second script")
	flag.Parse()

	firstArgs, secondArgs := strings.Fields(*firstArgsFlag), strings.Fields(*secondArgsFlag)
	workDir, err := os.MkdirTemp("", "clix-bench-go-cache-*")
	if err != nil {
		return err
	}
	defer func() {
		if removeError := removeTree(workDir); removeError != nil {
			fmt.Fprintln(os.Stderr, "warning: cleanup:", removeError)
		}
	}()

	clixBinary := filepath.Join(workDir, "clix")
	if output, err := exec.Command("go", "build", "-o", clixBinary, ".").CombinedOutput(); err != nil {
		return fmt.Errorf("building clix: %w: %s", err, strings.TrimSpace(string(output)))
	}
	firstScript, firstError := writeScript(filepath.Join(workDir, "script1"), *firstTarget)
	secondScript, secondError := writeScript(filepath.Join(workDir, "script2"), *secondTarget)
	versionCmd := exec.Command("go", "version")
	versionCmd.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	goVersion, versionError := versionCmd.Output()
	if err := errors.Join(firstError, secondError, versionError); err != nil {
		return err
	}

	var durations [6][]time.Duration
	var phases [3][]phaseTiming
	var isolatedSizes [2][3]int64
	var sharedDownloadBytes, sharedOtherBytes int64
	var firstZips, secondZips map[string]int64

	for i := 0; i < *iterations; i++ {
		iterationDir := filepath.Join(workDir, fmt.Sprintf("iter-%d", i))
		isolatedFirst := filepath.Join(iterationDir, "iso-1")
		isolatedSecond := filepath.Join(iterationDir, "iso-2")
		sharedFirst := filepath.Join(iterationDir, "shared-1")
		sharedSecond := filepath.Join(iterationDir, "shared-2")
		sharedDownload := filepath.Join(iterationDir, "shared-dl")
		splitDownload := filepath.Join(iterationDir, "split-dl")
		splitFirst := filepath.Join(iterationDir, "split-1")
		splitSecondCold := filepath.Join(iterationDir, "split-2-cold")
		splitSecondShared := filepath.Join(iterationDir, "split-2-shared")

		links := [4][2]string{
			{sharedFirst, sharedDownload},
			{sharedSecond, sharedDownload},
			{splitFirst, splitDownload},
			{splitSecondShared, splitDownload},
		}
		for _, pair := range links {
			if err := linkDownloadDir(pair[0], pair[1]); err != nil {
				return err
			}
		}

		clixSteps := [6]struct {
			script, dir string
			args        []string
		}{
			{firstScript, isolatedFirst, firstArgs},
			{secondScript, isolatedSecond, secondArgs},
			{firstScript, isolatedFirst, firstArgs},
			{secondScript, isolatedSecond, secondArgs},
			{firstScript, sharedFirst, firstArgs},
			{secondScript, sharedSecond, secondArgs},
		}
		for index, step := range clixSteps {
			cmd := exec.Command(clixBinary, append([]string{step.script}, step.args...)...)
			cmd.Env, cmd.Stdout, cmd.Stderr = cacheEnvironment(step.dir), io.Discard, io.Discard
			start := time.Now()
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("iteration %d step %d: %w", i, index, err)
			}
			durations[index] = append(durations[index], time.Since(start))
		}

		if i == 0 {
			var sharedFirstSizes, sharedSecondSizes [3]int64
			var sizeErrors [5]error
			isolatedSizes[0], firstZips, sizeErrors[0] = measureCache(isolatedFirst)
			isolatedSizes[1], secondZips, sizeErrors[1] = measureCache(isolatedSecond)
			sharedFirstSizes, _, sizeErrors[2] = measureCache(sharedFirst)
			sharedSecondSizes, _, sizeErrors[3] = measureCache(sharedSecond)
			sharedDownloadBytes, _, sizeErrors[4] = walkBytes(sharedDownload, false)
			if err := errors.Join(sizeErrors[:]...); err != nil {
				return err
			}
			sharedOtherBytes = sharedFirstSizes[1] + sharedFirstSizes[2] + sharedSecondSizes[1] + sharedSecondSizes[2]
		}

		splitSteps := [3]struct {
			target, dir string
			args        []string
		}{
			{*firstTarget, splitFirst, firstArgs},
			{*secondTarget, splitSecondCold, secondArgs},
			{*secondTarget, splitSecondShared, secondArgs},
		}
		for index, step := range splitSteps {
			phase, err := runPhaseSplit(step.target, step.args, step.dir)
			if err != nil {
				return fmt.Errorf("iteration %d split %d: %w", i, index, err)
			}
			phases[index] = append(phases[index], phase)
		}
		if err := removeTree(iterationDir); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, fmt.Sprintf("completed iteration %d/%d", i+1, *iterations))
	}

	printReport(
		strings.TrimSpace(string(goVersion)), *iterations, *firstTarget, *secondTarget,
		firstZips, secondZips, durations, isolatedSizes, sharedDownloadBytes, sharedOtherBytes, phases,
	)
	return nil
}

func linkDownloadDir(baseDir, sharedDownload string) error {
	cacheParent := filepath.Join(baseDir, "gopath", "pkg", "mod", "cache")
	if err := errors.Join(os.MkdirAll(sharedDownload, 0755), os.MkdirAll(cacheParent, 0755)); err != nil {
		return err
	}
	return os.Symlink(sharedDownload, filepath.Join(cacheParent, "download"))
}

func writeScript(path, target string) (string, error) {
	packagePath, version, _ := strings.Cut(target, "@")
	content := fmt.Sprintf(`#!/usr/bin/env clix
go:
  run: %s
  version: %s
`, packagePath, version)
	return path, os.WriteFile(path, []byte(content), 0755)
}

func cacheEnvironment(baseDir string) []string {
	gopath := filepath.Join(baseDir, "gopath")
	return append(os.Environ(),
		"GOPATH="+gopath,
		"GOMODCACHE="+filepath.Join(gopath, "pkg", "mod"),
		"GOCACHE="+filepath.Join(baseDir, "cache", "go-build"),
		"GOTOOLCHAIN=local",
	)
}

func runPhaseSplit(target string, args []string, baseDir string) (phaseTiming, error) {
	cmd := exec.Command("go", append([]string{"run", "-x", target}, args...)...)
	cmd.Env, cmd.Stdout = cacheEnvironment(baseDir), io.Discard
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return phaseTiming{}, err
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return phaseTiming{}, err
	}
	var firstCompile, firstLink time.Duration
	var downloads, compiled int
	scanner := bufio.NewScanner(stderrPipe)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line, elapsed := scanner.Text(), time.Since(start)
		switch {
		case strings.HasPrefix(line, "go: downloading "):
			downloads++
		case strings.Contains(line, "/compile "):
			compiled++
			if firstCompile == 0 {
				firstCompile = elapsed
			}
		case strings.Contains(line, "/link ") && firstLink == 0:
			firstLink = elapsed
		}
	}
	scanError, waitError, total := scanner.Err(), cmd.Wait(), time.Since(start)
	if err := errors.Join(scanError, waitError); err != nil {
		return phaseTiming{}, err
	}
	if firstCompile == 0 {
		firstCompile = total
	}
	if firstLink == 0 {
		firstLink = total
	}
	return phaseTiming{
		fetchAndLoad:  firstCompile,
		compile:       firstLink - firstCompile,
		linkAndRun:    total - firstLink,
		total:         total,
		downloads:     downloads,
		compiledCount: compiled,
	}, nil
}

func measureCache(baseDir string) ([3]int64, map[string]int64, error) {
	moduleCache := filepath.Join(baseDir, "gopath", "pkg", "mod")
	downloadPath := filepath.Join(moduleCache, "cache", "download")
	var downloadBytes int64
	var zips map[string]int64
	var downloadError error
	if info, err := os.Lstat(downloadPath); err == nil && info.Mode()&os.ModeSymlink == 0 {
		downloadBytes, zips, downloadError = walkBytes(downloadPath, true)
	}
	moduleTotal, _, moduleError := walkBytes(moduleCache, false)
	buildBytes, _, buildError := walkBytes(filepath.Join(baseDir, "cache", "go-build"), false)
	return [3]int64{downloadBytes, moduleTotal - downloadBytes, buildBytes}, zips, errors.Join(downloadError, moduleError, buildError)
}

func walkBytes(root string, collectZips bool) (int64, map[string]int64, error) {
	var total int64
	zips := map[string]int64{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkError error) error {
		if walkError != nil || entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			if os.IsNotExist(walkError) {
				return nil
			}
			return walkError
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if collectZips && strings.HasSuffix(entry.Name(), ".zip") {
			relativePath, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			zips[relativePath] = info.Size()
		}
		return nil
	})
	return total, zips, err
}

func removeTree(root string) error {
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkError error) error {
		if walkError != nil || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		mode := fs.FileMode(0644)
		if entry.IsDir() {
			mode = 0755
		}
		return os.Chmod(path, mode)
	}); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.RemoveAll(root)
}

func printReport(
	goVersion string,
	iterations int,
	firstTarget, secondTarget string,
	firstZips, secondZips map[string]int64,
	durations [6][]time.Duration,
	isolatedSizes [2][3]int64,
	sharedDownloadBytes, sharedOtherBytes int64,
	phases [3][]phaseTiming,
) {
	var overlapCount int
	var overlapBytes, secondZipBytes int64
	for key, size := range secondZips {
		secondZipBytes += size
		if _, ok := firstZips[key]; ok {
			overlapCount++
			overlapBytes += size
		}
	}
	toMiB := func(bytes int64) float64 { return float64(bytes) / (1024 * 1024) }
	percent := func(part, whole float64) float64 {
		if whole == 0 {
			return 0
		}
		return 100 * part / whole
	}
	medianOf := func(values []time.Duration) (time.Duration, time.Duration, time.Duration) {
		sorted := slices.Clone(values)
		slices.Sort(sorted)
		return sorted[len(sorted)/2], sorted[0], sorted[len(sorted)-1]
	}
	fmt.Println(fmt.Sprintf("Environment: %s, %s/%s, %d CPUs (GOMAXPROCS=%d), runs=%d",
		goVersion, runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0), iterations))
	fmt.Println(fmt.Sprintf("Script 1: %s (zips=%d, compiled_packages=%d)",
		firstTarget, len(firstZips), phases[0][0].compiledCount))
	fmt.Println(fmt.Sprintf("Script 2: %s (zips=%d, compiled_packages=%d)",
		secondTarget, len(secondZips), phases[1][0].compiledCount))
	fmt.Println(fmt.Sprintf("Zip overlap (1 -> 2): %d/%d zips (%.1f%%), %.1f/%.1f MiB (%.1f%%)",
		overlapCount, len(secondZips), percent(float64(overlapCount), float64(len(secondZips))),
		toMiB(overlapBytes), toMiB(secondZipBytes), percent(float64(overlapBytes), float64(secondZipBytes))))

	labels := [6]string{
		"Isolated: Script 1 first run",
		"Isolated: Script 2 first run",
		"Isolated: Script 1 repeat",
		"Isolated: Script 2 repeat",
		"Shared download: Script 1 first run",
		"Shared download: Script 2 first run",
	}
	for index, label := range labels {
		median, minimum, maximum := medianOf(durations[index])
		fmt.Println(fmt.Sprintf("  %-38s median=%.2fs (min=%.2fs, max=%.2fs)",
			label+":", median.Seconds(), minimum.Seconds(), maximum.Seconds()))
	}

	firstTotal := isolatedSizes[0][0] + isolatedSizes[0][1] + isolatedSizes[0][2]
	secondTotal := isolatedSizes[1][0] + isolatedSizes[1][1] + isolatedSizes[1][2]
	fmt.Println("Cache sizes (MiB):")
	fmt.Println(fmt.Sprintf("  Isolated Script 1: download=%.1f extracted=%.1f gocache=%.1f total=%.1f",
		toMiB(isolatedSizes[0][0]), toMiB(isolatedSizes[0][1]), toMiB(isolatedSizes[0][2]), toMiB(firstTotal)))
	fmt.Println(fmt.Sprintf("  Isolated Script 2: download=%.1f extracted=%.1f gocache=%.1f total=%.1f",
		toMiB(isolatedSizes[1][0]), toMiB(isolatedSizes[1][1]), toMiB(isolatedSizes[1][2]), toMiB(secondTotal)))
	fmt.Println(fmt.Sprintf("  Isolated combined: %.1f MiB | Shared combined: %.1f MiB (shared_download=%.1f other=%.1f)",
		toMiB(firstTotal+secondTotal), toMiB(sharedDownloadBytes+sharedOtherBytes),
		toMiB(sharedDownloadBytes), toMiB(sharedOtherBytes)))

	fmt.Println(fmt.Sprintf("Phase split (median of %d runs, go run -x):", iterations))
	phaseLabels := [3]string{"Script 1 cold", "Script 2 cold (isolated)", "Script 2 shared download"}
	for index, label := range phaseLabels {
		var fetches, compiles, links, totals []time.Duration
		for _, phase := range phases[index] {
			fetches = append(fetches, phase.fetchAndLoad)
			compiles = append(compiles, phase.compile)
			links = append(links, phase.linkAndRun)
			totals = append(totals, phase.total)
		}
		fetchMedian, _, _ := medianOf(fetches)
		compileMedian, _, _ := medianOf(compiles)
		linkMedian, _, _ := medianOf(links)
		totalMedian, _, _ := medianOf(totals)
		fmt.Println(fmt.Sprintf("  %-26s total=%.2fs | fetch+extract=%.2fs (%.0f%%) | compile=%.2fs (%.0f%%) | link+run=%.2fs (%.0f%%) | downloads=%d",
			label+":", totalMedian.Seconds(),
			fetchMedian.Seconds(), percent(fetchMedian.Seconds(), totalMedian.Seconds()),
			compileMedian.Seconds(), percent(compileMedian.Seconds(), totalMedian.Seconds()),
			linkMedian.Seconds(), percent(linkMedian.Seconds(), totalMedian.Seconds()),
			phases[index][0].downloads))
	}
}

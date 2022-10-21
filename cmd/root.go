package cmd

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/briandowns/spinner"
	"github.com/chromedp/chromedp"
	"github.com/manifoldco/promptui"
	"github.com/nicoxiang/geektime-downloader/internal/audio"
	"github.com/nicoxiang/geektime-downloader/internal/config"
	"github.com/nicoxiang/geektime-downloader/internal/geektime"
	"github.com/nicoxiang/geektime-downloader/internal/geektime/response"
	"github.com/nicoxiang/geektime-downloader/internal/markdown"
	"github.com/nicoxiang/geektime-downloader/internal/pdf"
	"github.com/nicoxiang/geektime-downloader/internal/pkg/filenamify"
	pgt "github.com/nicoxiang/geektime-downloader/internal/pkg/geektime"
	"github.com/nicoxiang/geektime-downloader/internal/video"
	"github.com/spf13/cobra"
)

var (
	phone              string
	gcid               string
	gcess              string
	concurrency        int
	downloadFolder     string
	sp                 *spinner.Spinner
	currentProduct     geektime.Product
	quality            string
	downloadComments   bool
	sourceType         int // video source type
	columnOutputType   int
	productTypeOptions = make([]productTypeSelectOption, 4)
)

type productTypeSelectOption struct {
	Text  string
	Value int
}

func init() {
	userHomeDir, _ := os.UserHomeDir()
	concurrency = int(math.Ceil(float64(runtime.NumCPU()) / 2.0))
	defaultDownloadFolder := filepath.Join(userHomeDir, config.GeektimeDownloaderFolder)
	productTypeOptions[0] = productTypeSelectOption{"普通课程", 1}
	productTypeOptions[1] = productTypeSelectOption{"每日一课", 2}
	productTypeOptions[2] = productTypeSelectOption{"大厂案例", 4}
	productTypeOptions[3] = productTypeSelectOption{"训练营", 5}

	rootCmd.Flags().StringVarP(&phone, "phone", "u", "", "你的极客时间账号(手机号)")
	rootCmd.Flags().StringVar(&gcid, "gcid", "", "极客时间 cookie 值 gcid")
	rootCmd.Flags().StringVar(&gcess, "gcess", "", "极客时间 cookie 值 gcess")
	rootCmd.Flags().StringVarP(&downloadFolder, "folder", "f", defaultDownloadFolder, "专栏和视频课的下载目标位置")
	rootCmd.Flags().StringVarP(&quality, "quality", "q", "sd", "下载视频清晰度(ld 标清, sd 高清, hd 超清)")
	rootCmd.Flags().BoolVar(&downloadComments, "comments", true, "是否需要专栏的第一页评论")
	rootCmd.Flags().IntVar(&columnOutputType, "output", 1, "专栏的输出内容(1 pdf, 2 markdown, 4 audio)可自由组合")

	rootCmd.MarkFlagsMutuallyExclusive("phone", "gcid")
	rootCmd.MarkFlagsMutuallyExclusive("phone", "gcess")
	rootCmd.MarkFlagsRequiredTogether("gcid", "gcess")

	sp = spinner.New(spinner.CharSets[4], 500*time.Millisecond)
}

var rootCmd = &cobra.Command{
	Use:   "geektime-downloader",
	Short: "Geektime-downloader is used to download geek time lessons",
	Run: func(cmd *cobra.Command, args []string) {
		if quality != "ld" && quality != "sd" && quality != "hd" {
			exitWithMsg("argument 'quality' is not valid")
		}
		if columnOutputType <= 0 || columnOutputType >= 8 {
			exitWithMsg("argument 'columnOutputType' is not valid")
		}
		var readCookies []*http.Cookie
		if phone != "" {
			rc, err := config.ReadCookieFromConfigFile(phone)
			checkError(err)
			readCookies = rc
		} else if gcid != "" && gcess != "" {
			readCookies = readCookiesFromInput()
		} else {
			exitWithMsg("argument 'phone' or cookie value is not valid")
		}
		if readCookies == nil {
			prompt := promptui.Prompt{
				Label: "请输入密码",
				Validate: func(s string) error {
					if strings.TrimSpace(s) == "" {
						return errors.New("密码不能为空")
					}
					return nil
				},
				Mask:        '*',
				HideEntered: true,
			}
			pwd, err := prompt.Run()
			checkError(err)
			sp.Prefix = "[ 正在登录... ]"
			sp.Start()
			readCookies, err = geektime.Login(phone, pwd)
			if err != nil {
				sp.Stop()
				checkError(err)
			}
			err = config.WriteCookieToConfigFile(phone, readCookies)
			checkError(err)
			sp.Stop()
			fmt.Fprintln(os.Stderr, "登录成功")
		}
		geektime.InitClient(readCookies)

		// first time auth check
		if err := geektime.Auth(); err != nil {
			checkError(pgt.ErrAuthFailed)
		}

		selectProductType(cmd.Context())
	},
}

func selectProductType(ctx context.Context) {
	templates := &promptui.SelectTemplates{
		Label:    "{{ . }}",
		Active:   "{{ `>` | red }} {{ .Text | red }}",
		Inactive: "{{ .Text }}",
	}
	prompt := promptui.Select{
		Label:        "请选择想要下载的产品类型",
		Items:        productTypeOptions,
		Templates:    templates,
		Size:         len(productTypeOptions),
		HideSelected: true,
		Stdout:       NoBellStdout,
	}
	index, _, err := prompt.Run()
	checkError(err)
	sourceType = productTypeOptions[index].Value
	letInputProductID(ctx)
}

func letInputProductID(ctx context.Context) {
	prompt := promptui.Prompt{
		Label: fmt.Sprintf("请输入%s的课程 ID", findProductTypeText(sourceType)),
		Validate: func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("课程 ID 不能为空")
			}
			if _, err := strconv.Atoi(s); err != nil {
				return errors.New("课程 ID 格式不合法")
			}
			return nil
		},
		HideEntered: true,
	}
	s, err := prompt.Run()
	checkError(err)

	// ignore, because checked before
	id, _ := strconv.Atoi(s)

	if sourceType == 1 || sourceType == 5 {
		// when source type is normal cource or university cource
		// choose download all or download specified article
		loadProduct(ctx, id)
		productOps(ctx)
	} else {
		// when source type is daily lesson or qconplus,
		// input id means product id
		// download video directly
		productInfo, err := geektime.PostV3ProductInfo(id)
		checkError(err)

		if checkProductType(productInfo.Data.Info.Type, sourceType) {
			projectDir, err := mkDownloadProjectDir(downloadFolder, phone, gcid, productInfo.Data.Info.Title)
			checkError(err)

			err = video.DownloadArticleVideo(ctx,
				productInfo.Data.Info.Article.ID,
				sourceType,
				projectDir,
				quality,
				concurrency)

			checkError(err)
		}
		letInputProductID(ctx)
	}
}

func loadProduct(ctx context.Context, productID int) {
	sp.Prefix = "[ 正在加载课程信息... ]"
	sp.Start()
	var p geektime.Product
	var err error
	if sourceType == 5 {
		p, err = geektime.GetMyClassProduct(productID)
		// university don't need check product type
		// if input invalid id, access mark is 0
	} else if sourceType == 1 {
		p, err = geektime.PostV3ColumnInfo(productID)
		if err == nil {
			c := checkProductType(p.Type, sourceType)
			// if check product type fail, re-input product
			if !c {
				sp.Stop()
				letInputProductID(ctx)
			}
		}
	}

	if err != nil {
		sp.Stop()
		checkError(err)
	}
	sp.Stop()
	if !p.Access {
		fmt.Fprint(os.Stderr, "尚未购买该课程\n")
		letInputProductID(ctx)
	}
	currentProduct = p
}

func productOps(ctx context.Context) {
	options := make([]productTypeSelectOption, 3)
	options[0] = productTypeSelectOption{"重新选择课程", 0}
	if isText() {
		options[1] = productTypeSelectOption{"下载当前专栏所有文章", 1}
		options[2] = productTypeSelectOption{"选择文章", 2}
	} else if isVideo() {
		options[1] = productTypeSelectOption{"下载所有视频", 1}
		options[2] = productTypeSelectOption{"选择视频", 2}
	}
	templates := &promptui.SelectTemplates{
		Label:    "{{ . }}",
		Active:   "{{ `>` | red }} {{ .Text | red }}",
		Inactive: "{{if eq .Value 0}} {{ .Text | green }} {{else}} {{ .Text }} {{end}}",
	}
	prompt := promptui.Select{
		Label:        fmt.Sprintf("当前选中的专栏为: %s, 请继续选择：", currentProduct.Title),
		Items:        options,
		Templates:    templates,
		Size:         len(options),
		HideSelected: true,
		Stdout:       NoBellStdout,
	}
	index, _, err := prompt.Run()
	checkError(err)

	switch index {
	case 0:
		selectProductType(ctx)
	case 1:
		handleDownloadAll(ctx)
	case 2:
		selectArticle(ctx)
	}
}

func selectArticle(ctx context.Context) {
	loadArticles()
	items := []geektime.Article{
		{
			AID:   -1,
			Title: "返回上一级",
		},
	}
	items = append(items, currentProduct.Articles...)
	templates := &promptui.SelectTemplates{
		Label:    "{{ . }}",
		Active:   "{{ `>` | red }} {{ .Title | red }}",
		Inactive: "{{if eq .AID -1}} {{ .Title | green }} {{else}} {{ .Title }} {{end}}",
	}
	prompt := promptui.Select{
		Label:        "请选择文章: ",
		Items:        items,
		Templates:    templates,
		Size:         20,
		HideSelected: true,
		CursorPos:    0,
		Stdout:       NoBellStdout,
	}
	index, _, err := prompt.Run()
	checkError(err)
	handleSelectArticle(ctx, index)
}

func handleSelectArticle(ctx context.Context, index int) {
	if index == 0 {
		productOps(ctx)
	}
	a := currentProduct.Articles[index-1]

	projectDir, err := mkDownloadProjectDir(downloadFolder, phone, gcid, currentProduct.Title)
	checkError(err)
	downloadArticle(ctx, a, projectDir)
	fmt.Printf("\r%s 下载完成", a.Title)
	time.Sleep(time.Second)
	selectArticle(ctx)
}

func handleDownloadAll(ctx context.Context) {
	loadArticles()
	projectDir, err := mkDownloadProjectDir(downloadFolder, phone, gcid, currentProduct.Title)
	checkError(err)
	downloaded, err := findDownloadedArticleFileNames(projectDir)
	checkError(err)
	if isText() {
		fmt.Printf("正在下载专栏 《%s》 中的所有文章\n", currentProduct.Title)
		var i int

		var chromedpCtx context.Context
		var cancel context.CancelFunc

		if columnOutputType&1 == 1 {
			chromedpCtx, cancel = chromedp.NewContext(ctx)
			// start the browser
			err := chromedp.Run(chromedpCtx)
			checkError(err)
			defer cancel()
		}

		for _, l := range currentProduct.Lessons {
			prompt(currentProduct.Total, i)
			fmt.Printf("\t%s\n", l.ChapterName)

			chapterName := filenamify.Filenamify(strconv.Itoa(l.IndexNo) + "." + l.ChapterName)
			chapterDir := filepath.Join(projectDir, chapterName)
			err := os.MkdirAll(chapterDir, os.ModePerm)
			checkError(err)

			for _, a := range l.Articles {
				i++
				prompt(currentProduct.Total, i)
				fmt.Printf("\t\t%s\n", a.ArticleTitle)

				articleTitle := filenamify.Filenamify(a.ArticleTitle)
				var b int
				if _, exists := downloaded[chapterName][articleTitle+pdf.PDFExtension]; exists {
					b = setBit(b, 0)
				}
				if _, exists := downloaded[chapterName][articleTitle+markdown.MDExtension]; exists {
					b = setBit(b, 1)
				}
				if _, exists := downloaded[chapterName][articleTitle+audio.MP3Extension]; exists {
					b = setBit(b, 2)
				}

				if b == columnOutputType {
					continue
				}

				var err error

				if columnOutputType&^b&1 == 1 {
					err = pdf.PrintArticlePageToPDF(chromedpCtx,
						a.ArticleID,
						chapterDir,
						articleTitle,
						geektime.SiteCookies,
						downloadComments,
					)
					if err != nil {
						// ensure chrome killed before os exit
						cancel()
						checkError(err)
					}
				}

				var articleInfo response.V1ArticleResponse
				needDownloadMD := (columnOutputType>>1)&^(b>>1)&1 == 1
				needDownloadAudio := (columnOutputType>>2)&^(b>>2)&1 == 1

				if needDownloadMD || needDownloadAudio {
					articleInfo, err = geektime.GetArticleInfo(a.ArticleID)
					checkError(err)
				}

				if needDownloadMD {
					err = markdown.Download(ctx, articleInfo.Data.ArticleContent, articleTitle, chapterDir, a.ArticleID, concurrency)
				}

				if needDownloadAudio {
					err = audio.DownloadAudio(ctx, articleInfo.Data.AudioDownloadURL, chapterDir, articleTitle)
				}

				checkError(err)
				time.Sleep(time.Duration(rand.Intn(2000)) * time.Millisecond)
			}
		}
	} else if isVideo() {
		var i int
		for _, l := range currentProduct.Lessons {
			prompt(currentProduct.Total, i)
			fmt.Printf("\t%s\n", l.ChapterName)

			chapterName := filenamify.Filenamify(strconv.Itoa(l.IndexNo) + "." + l.ChapterName)
			chapterDir := filepath.Join(projectDir, chapterName)
			err := os.MkdirAll(chapterDir, os.ModePerm)
			checkError(err)

			for _, a := range l.Articles {
				i++
				prompt(currentProduct.Total, i)

				articleTitle := filenamify.Filenamify(a.ArticleTitle) + video.TSExtension
				if _, ok := downloaded[chapterName][articleTitle]; ok {
					continue
				}
				if sourceType == 1 {
					err := video.DownloadArticleVideo(ctx, a.ArticleID, sourceType, chapterDir, quality, concurrency)
					checkError(err)
				} else if sourceType == 5 {
					if a.VideoTime <= 0 {
						continue
					}
					err := video.DownloadUniversityVideo(ctx, a.ArticleID, currentProduct, chapterDir, quality, concurrency)
					checkError(err)
				}
			}
		}
	}
	selectProductType(ctx)
}

func prompt(total int, i int) {
	fmt.Printf("\r[%s] 正在下载 %d/%d", time.Now().Format("2006-01-02 15:04:05"), i, total)
}

func loadArticles() {
	if len(currentProduct.Articles) > 0 {
		return
	}

	sp.Prefix = "[ 正在加载文章列表... ]"
	sp.Start()
	articles, err := geektime.PostV1ColumnArticles(strconv.Itoa(currentProduct.ID))
	checkError(err)
	currentProduct.Articles = articles
	currentProduct.Total = len(articles)

	// 上面的接口查出的 Article 列表，是没有按 Chapter 组织的，虽然包含 ChapterID，但是没有 ChapterName
	// 由于没有直接查询 Chapter 信息的接口，而下面查询 Article 详情的接口，包含 ChapterName，故以此间接实现按 Chapter 组织 Article 列表
	for _, article := range articles {
		a, err := geektime.PostV3ArticleInfo(article.AID)
		checkError(err)

		if len(currentProduct.Lessons) == 0 ||
			currentProduct.Lessons[len(currentProduct.Lessons)-1].ChapterID != a.Data.Info.ChapterID {
			currentProduct.Lessons = append(currentProduct.Lessons,
				response.Lesson{
					ChapterName: a.Data.Info.ChapterTitle,
					ChapterID:   a.Data.Info.ChapterID,
					IndexNo:     len(currentProduct.Lessons) + 1,
				})
		}

		lesson := &currentProduct.Lessons[len(currentProduct.Lessons)-1]
		lesson.Articles = append(lesson.Articles,
			response.Article{
				ArticleID:    a.Data.Info.ID,
				ArticleTitle: a.Data.Info.Title,
				IndexNo:      len(lesson.Articles) + 1,
			})
	}

	sp.Stop()
}

func downloadArticle(ctx context.Context, article geektime.Article, projectDir string) {
	l, err := getLesson(article.AID)
	checkError(err)

	chapterName := filenamify.Filenamify(strconv.Itoa(l.IndexNo) + "." + l.ChapterName)
	chapterDir := filepath.Join(projectDir, chapterName)
	err = os.MkdirAll(chapterDir, os.ModePerm)
	checkError(err)

	if isText() {
		sp.Prefix = fmt.Sprintf("[ 正在下载 《%s》... ]", article.Title)
		sp.Start()

		if columnOutputType&1 == 1 {
			chromedpCtx, cancel := chromedp.NewContext(ctx)
			// start the browser
			err := chromedp.Run(chromedpCtx)
			checkError(err)
			defer cancel()
			err = pdf.PrintArticlePageToPDF(chromedpCtx,
				article.AID,
				chapterDir,
				article.Title,
				geektime.SiteCookies,
				downloadComments,
			)
			if err != nil {
				sp.Stop()
				// ensure chrome killed before os exit
				cancel()
				checkError(err)
			}
		}

		var articleInfo response.V1ArticleResponse
		var err error
		needDownloadMD := (columnOutputType>>1)&1 == 1
		needDownloadAudio := (columnOutputType>>2)&1 == 1

		if needDownloadMD || needDownloadAudio {
			articleInfo, err = geektime.GetArticleInfo(article.AID)
			checkError(err)
		}

		if needDownloadMD {
			err = markdown.Download(ctx, articleInfo.Data.ArticleContent, article.Title, chapterDir, article.AID, concurrency)
		}

		if needDownloadAudio {
			err = audio.DownloadAudio(ctx, articleInfo.Data.AudioDownloadURL, chapterDir, article.Title)
		}

		checkError(err)

		sp.Stop()
	} else if isVideo() {
		if sourceType == 1 {
			err := video.DownloadArticleVideo(ctx, article.AID, sourceType, chapterDir, quality, concurrency)
			checkError(err)
		} else if sourceType == 5 {
			err := video.DownloadUniversityVideo(ctx, article.AID, currentProduct, chapterDir, quality, concurrency)
			checkError(err)
		}
	}
}

func getLesson(aid int) (*response.Lesson, error) {
	for i := range currentProduct.Lessons {
		for _, a := range currentProduct.Lessons[i].Articles {
			if a.ArticleID == aid {
				return &currentProduct.Lessons[i], nil
			}
		}
	}
	return nil, errors.New("not found")
}

func isText() bool {
	return currentProduct.Type == string(geektime.ProductTypeColumn) ||
		currentProduct.Type == string(geektime.ProductTypeP29)
}

func isVideo() bool {
	return currentProduct.Type == string(geektime.ProductTypeNormalVideo) ||
		currentProduct.Type == string(geektime.ProductTypeUniversityVideo) ||
		currentProduct.Type == string(geektime.ProductTypeC6Video)
}

// Sets the bit at pos in the integer n.
func setBit(n int, pos uint) int {
	n |= 1 << pos
	return n
}

func readCookiesFromInput() []*http.Cookie {
	oneyear := time.Now().Add(180 * 24 * time.Hour)
	cookies := make([]*http.Cookie, 2)
	m := make(map[string]string, 2)
	m[pgt.GCID] = gcid
	m[pgt.GCESS] = gcess
	c := 0
	for k, v := range m {
		cookies[c] = &http.Cookie{
			Name:     k,
			Value:    v,
			Domain:   pgt.GeekBangCookieDomain,
			HttpOnly: true,
			Expires:  oneyear,
		}
		c++
	}
	return cookies
}

func findDownloadedArticleFileNames(projectDir string) (map[string]map[string]struct{}, error) {
	files, err := os.ReadDir(projectDir)
	res := make(map[string]map[string]struct{}, len(files))
	if err != nil {
		return res, err
	}
	if len(files) == 0 {
		return res, nil
	}
	for _, f := range files {
		res[f.Name()] = make(map[string]struct{})
		if f.IsDir() {
			subFiles, err := os.ReadDir(filepath.Join(projectDir, f.Name()))
			if err != nil {
				return res, err
			}
			for _, subFile := range subFiles {
				res[f.Name()][subFile.Name()] = struct{}{}
			}
		}
	}
	return res, nil
}

func mkDownloadProjectDir(downloadFolder, phone, gcid, projectName string) (string, error) {
	userName := phone
	if gcid != "" {
		userName = gcid
	}
	path := filepath.Join(downloadFolder, userName, filenamify.Filenamify(projectName))
	err := os.MkdirAll(path, os.ModePerm)
	if err != nil {
		return "", err
	}
	return path, nil
}

func checkProductType(productType string, sourceType int) bool {
	if (productType == string(geektime.ProductTypeDailyLesson) && sourceType == 2) ||
		(productType == string(geektime.ProductTypeQCONPlus) && sourceType == 4) ||
		(productType == string(geektime.ProductTypeColumn) && sourceType == 1) ||
		(productType == string(geektime.ProductTypeNormalVideo) && sourceType == 1) ||
		(productType == string(geektime.ProductTypeC6Video) && sourceType == 1) ||
		(productType == string(geektime.ProductTypeP29) && sourceType == 1) {
		return true
	}
	fmt.Fprintf(os.Stderr, "\n\n输入的课程 ID 有误, productType: %s, sourceType: %d\n\n", productType, sourceType)
	return false
}

func findProductTypeText(sourceType int) string {
	for _, option := range productTypeOptions {
		if option.Value == sourceType {
			return option.Text
		}
	}
	return ""
}

// Execute ...
func Execute() {
	ctx, cancel := context.WithCancel(context.Background())
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	defer func() {
		signal.Stop(c)
		cancel()
	}()
	go func() {
		select {
		case <-c:
			cancel()
		case <-ctx.Done():
		}
	}()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		checkError(err)
	}
}

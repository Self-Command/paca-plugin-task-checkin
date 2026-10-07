package worker
import("bytes";"image";"image/color";"image/jpeg";"image/png";"testing")
func TestPhotoValidationAndNormalization(t *testing.T){
 if _,err:=normalizePhoto([]byte("not an image"));err==nil{t.Fatal("invalid content accepted")}
 source:=image.NewRGBA(image.Rect(0,0,3000,20));source.Set(0,0,color.White);var raw bytes.Buffer
 if err:=png.Encode(&raw,source);err!=nil{t.Fatal(err)}
 result,err:=normalizePhoto(raw.Bytes());if err!=nil{t.Fatal(err)}
 config,err:=jpeg.DecodeConfig(bytes.NewReader(result));if err!=nil||config.Width!=2048{t.Fatalf("normalization failed: %+v %v",config,err)}
 if bytes.Contains(result,[]byte("Exif")){t.Fatal("EXIF retained")}
}

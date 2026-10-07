const MiB = 1024 * 1024;
const MAX_PIXELS = 25_000_000;
const TARGET_BYTES = 512 * 1024;
export const MAX_UPLOAD_BYTES = MiB;

function aborted(signal?: AbortSignal): void {
  if (signal?.aborted) throw new DOMException("照片处理已取消", "AbortError");
}

// Inspect supported headers before allocating a decoded bitmap.
function imageInfo(data: ArrayBuffer): { width: number; height: number; orientation: number } {
  const b = new Uint8Array(data); const v = new DataView(data);
  let width = 0, height = 0, orientation = 1;
  if (b.length >= 24 && b[0] === 137 && b[1] === 80 && b[2] === 78 && b[3] === 71) {
    width = v.getUint32(16); height = v.getUint32(20);
  } else if (b.length >= 30 && String.fromCharCode(...b.slice(0, 4)) === "RIFF" && String.fromCharCode(...b.slice(8, 12)) === "WEBP") {
    const kind = String.fromCharCode(...b.slice(12, 16));
    if (kind === "VP8X") {
      width = 1 + b[24] + (b[25] << 8) + (b[26] << 16);
      height = 1 + b[27] + (b[28] << 8) + (b[29] << 16);
    } else if (kind === "VP8 ") {
      width = v.getUint16(26, true) & 16383; height = v.getUint16(28, true) & 16383;
    } else if (kind === "VP8L" && b[20] === 47) {
      const packed = v.getUint32(21, true);
      width = 1 + (packed & 16383); height = 1 + ((packed >>> 14) & 16383);
    }
  } else if (b.length >= 4 && b[0] === 255 && b[1] === 216) {
    let p = 2;
    while (p + 4 <= b.length && b[p] === 255) {
      const marker = b[p + 1];
      if (marker === 218 || marker === 217) break;
      if (marker === 255) { p++; continue; }
      const length = v.getUint16(p + 2);
      if (length < 2 || p + 2 + length > b.length) break;
      if ([192,193,194,195,197,198,199,201,202,203,205,206,207].includes(marker) && length >= 7) {
        height = v.getUint16(p + 5); width = v.getUint16(p + 7);
      }
      if (marker === 225 && length >= 16 && String.fromCharCode(...b.slice(p + 4, p + 10)) === "Exif\0\0") {
        const t = p + 10; const little = b[t] === 73 && b[t + 1] === 73;
        if ((little || (b[t] === 77 && b[t + 1] === 77)) && v.getUint16(t + 2, little) === 42) {
          const ifd = t + v.getUint32(t + 4, little); const end = p + 2 + length;
          if (ifd >= t && ifd + 2 <= end) {
            const count = v.getUint16(ifd, little);
            for (let i = 0; i < count && ifd + 2 + (i + 1) * 12 <= end; i++) {
              const entry = ifd + 2 + i * 12;
              if (v.getUint16(entry, little) === 274 && v.getUint16(entry + 2, little) === 3 && v.getUint32(entry + 4, little) === 1) orientation = v.getUint16(entry + 8, little);
            }
          }
        }
      }
      p += length + 2;
    }
  }
  if (!width || !height) throw new Error("照片格式无效，请重新选择照片。");
  if (width * height > MAX_PIXELS) throw new Error("照片不能超过 2500 万像素，请选择较小照片。");
  return { width, height, orientation };
}

async function decode(file: File): Promise<{ image: CanvasImageSource; width: number; height: number; close: () => void }> {
  if (typeof createImageBitmap === "function") {
    try {
      const bitmap = await createImageBitmap(file, { imageOrientation: "from-image" });
      return { image: bitmap, width: bitmap.width, height: bitmap.height, close: () => bitmap.close() };
    } catch { /* System WebViews without bitmap decoding use the image decoder. */ }
  }
  const url = URL.createObjectURL(file); const image = new Image();
  try {
    await new Promise<void>((resolve, reject) => { image.onload = () => resolve(); image.onerror = () => reject(new Error("照片无法读取，请重新选择照片。")); image.src = url; });
    return { image, width: image.naturalWidth, height: image.naturalHeight, close: () => { image.src = ""; URL.revokeObjectURL(url); } };
  } catch (error) { URL.revokeObjectURL(url); throw error; }
}

function jpeg(canvas: HTMLCanvasElement, quality: number): Promise<Blob> {
  return new Promise((resolve, reject) => canvas.toBlob(blob => blob?.type === "image/jpeg" ? resolve(blob) : reject(new Error("照片压缩失败，请重新选择照片。")), "image/jpeg", quality));
}

export async function compressPhoto(file: File, signal?: AbortSignal): Promise<File> {
  aborted(signal);
  if (!file.size || file.size > 10 * MiB) throw new Error("照片不能为空，且不能超过 10 MiB。");
  if (!["image/jpeg", "image/png", "image/webp"].includes(file.type)) throw new Error("请选择 JPEG、PNG 或 WebP 格式照片。");
  const info = imageInfo(await file.arrayBuffer()); aborted(signal);
  const decoded = await decode(file); const canvas = document.createElement("canvas");
  try {
    aborted(signal);
    if (!decoded.width || !decoded.height || decoded.width * decoded.height > MAX_PIXELS) throw new Error("照片不能超过 2500 万像素。");
    const context = canvas.getContext("2d");
    if (!context) throw new Error("当前设备无法处理照片，请更新系统后重试。");
    const sourceEdge = Math.max(decoded.width, decoded.height);
    let best: Blob | null = null;
    const edges = [...new Set([2048,1792,1536,1280,1024].map(edge => Math.min(edge, sourceEdge)))];
    for (const edge of edges) {
      const ratio = edge / sourceEdge;
      canvas.width = Math.max(1, Math.round(decoded.width * ratio));
      canvas.height = Math.max(1, Math.round(decoded.height * ratio));
      context.fillStyle = "#ffffff"; context.fillRect(0,0,canvas.width,canvas.height);
      context.drawImage(decoded.image,0,0,canvas.width,canvas.height);
      for (const quality of [0.82,0.76,0.70,0.65]) {
        aborted(signal); const candidate = await jpeg(canvas,quality); aborted(signal);
        if (!best || candidate.size < best.size) best = candidate;
        if (candidate.size <= TARGET_BYTES) break;
      }
      if (best && best.size <= TARGET_BYTES) break;
    }
    if (file.size <= MAX_UPLOAD_BYTES && sourceEdge <= 2048 && info.orientation === 1 && best && best.size > file.size) return file;
    if (!best || best.size > MAX_UPLOAD_BYTES) throw new Error("照片处理后仍过大，请选择较小照片。");
    return new File([best], file.name.replace(/\.[^.]*$/, "") + ".jpg", {type:"image/jpeg",lastModified:file.lastModified});
  } finally { decoded.close(); canvas.width = 0; canvas.height = 0; }
}

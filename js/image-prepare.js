// JPEGs larger than the upload limit are resized on the user's device.
// Other formats, especially animated images and transparency, are left intact.
export async function prepareOwnImage(file) {
    if (file.type !== 'image/jpeg' || file.size <= 2 * 1024 * 1024 || file.size > 40 * 1024 * 1024 || !globalThis.createImageBitmap) return file;
    let bitmap;
    try {
        bitmap = await createImageBitmap(file);
        if (!bitmap.width || !bitmap.height || bitmap.width * bitmap.height > 40_000_000) return file;
        const scale = Math.min(1, 2048 / Math.max(bitmap.width, bitmap.height));
        const canvas = document.createElement('canvas');
        canvas.width = Math.max(1, Math.round(bitmap.width * scale));
        canvas.height = Math.max(1, Math.round(bitmap.height * scale));
        const ctx = canvas.getContext('2d');
        if (!ctx) return file;
        ctx.drawImage(bitmap, 0, 0, canvas.width, canvas.height);
        const blob = await new Promise(resolve => canvas.toBlob(resolve, 'image/jpeg', 0.88));
        return blob && blob.type === 'image/jpeg' && blob.size < file.size ? new File([blob], file.name, { type: 'image/jpeg', lastModified: file.lastModified }) : file;
    } catch { return file; }
    finally { bitmap?.close(); }
}

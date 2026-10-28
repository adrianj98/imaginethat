package ai.alertd.imagethat.local;

import android.content.Context;
import android.content.pm.ProviderInfo;
import android.database.Cursor;
import android.database.MatrixCursor;
import android.os.CancellationSignal;
import android.os.ParcelFileDescriptor;
import android.provider.DocumentsContract.Document;
import android.provider.DocumentsContract.Root;
import android.provider.DocumentsProvider;
import java.io.File;
import java.io.FileNotFoundException;
import java.util.Arrays;

/**
 * Minimal directory-backed DocumentsProvider for tests, in Java because it runs
 * in the test APK's own process, which has no Kotlin runtime. Document ids are
 * "r" for the root and "r/relative/path" below it. The backing directory is
 * this package's cache dir (tests reach it only through the provider API).
 */
public class TestDocsProvider extends DocumentsProvider {
    public static final String AUTHORITY = "ai.alertd.imagethat.test.docs";

    private static final String[] COLUMNS = {
        Document.COLUMN_DOCUMENT_ID, Document.COLUMN_DISPLAY_NAME, Document.COLUMN_MIME_TYPE,
        Document.COLUMN_SIZE, Document.COLUMN_FLAGS, Document.COLUMN_LAST_MODIFIED,
    };

    private File base;

    /** Subclasses override to model providers without moveDocument. */
    protected boolean supportsMove() {
        return true;
    }

    protected String authority() {
        return AUTHORITY;
    }

    @Override
    public boolean onCreate() {
        return true;
    }

    @Override
    public void attachInfo(Context context, ProviderInfo info) {
        // DocumentsProvider insists on MANAGE_DOCUMENTS. The manifest omits it so the
        // instrumented app can obtain a URI grant without the system picker.
        ProviderInfo copy = new ProviderInfo(info);
        copy.readPermission = android.Manifest.permission.MANAGE_DOCUMENTS;
        copy.writePermission = android.Manifest.permission.MANAGE_DOCUMENTS;
        super.attachInfo(context, copy);
        base = new File(context.getCacheDir(), authority());
        base.mkdirs();
    }

    private File file(String id) throws FileNotFoundException {
        if (!id.equals("r") && !id.startsWith("r/")) throw new FileNotFoundException(id);
        File f = new File(base, id.substring(1));
        if (!f.exists()) throw new FileNotFoundException(id);
        return f;
    }

    private String idOf(File f) {
        String rel = base.toURI().relativize(f.toURI()).getPath();
        while (rel.endsWith("/")) rel = rel.substring(0, rel.length() - 1);
        return rel.isEmpty() ? "r" : "r/" + rel;
    }

    private void addRow(MatrixCursor c, File f) {
        int flags = Document.FLAG_SUPPORTS_DELETE | Document.FLAG_SUPPORTS_RENAME;
        if (supportsMove()) flags |= Document.FLAG_SUPPORTS_MOVE;
        flags |= f.isDirectory() ? Document.FLAG_DIR_SUPPORTS_CREATE : Document.FLAG_SUPPORTS_WRITE;
        c.newRow().add(Document.COLUMN_DOCUMENT_ID, idOf(f))
            .add(Document.COLUMN_DISPLAY_NAME, f.equals(base) ? "root" : f.getName())
            .add(Document.COLUMN_MIME_TYPE, f.isDirectory() ? Document.MIME_TYPE_DIR : "application/octet-stream")
            .add(Document.COLUMN_SIZE, f.isDirectory() ? null : f.length())
            .add(Document.COLUMN_FLAGS, flags)
            .add(Document.COLUMN_LAST_MODIFIED, f.lastModified());
    }

    @Override
    public Cursor queryRoots(String[] projection) {
        return new MatrixCursor(projection != null ? projection : new String[] {Root.COLUMN_ROOT_ID});
    }

    @Override
    public Cursor queryDocument(String documentId, String[] projection) throws FileNotFoundException {
        MatrixCursor c = new MatrixCursor(COLUMNS);
        addRow(c, file(documentId));
        return c;
    }

    @Override
    public Cursor queryChildDocuments(String parentDocumentId, String[] projection, String sortOrder)
            throws FileNotFoundException {
        MatrixCursor c = new MatrixCursor(COLUMNS);
        File[] kids = file(parentDocumentId).listFiles();
        if (kids != null) {
            Arrays.sort(kids);
            for (File k : kids) addRow(c, k);
        }
        return c;
    }

    @Override
    public ParcelFileDescriptor openDocument(String documentId, String mode, CancellationSignal signal)
            throws FileNotFoundException {
        return ParcelFileDescriptor.open(file(documentId), ParcelFileDescriptor.parseMode(mode));
    }

    @Override
    public String createDocument(String parentDocumentId, String mimeType, String displayName)
            throws FileNotFoundException {
        File f = new File(file(parentDocumentId), displayName);
        if (f.exists()) throw new IllegalStateException("exists: " + displayName);
        try {
            if (Document.MIME_TYPE_DIR.equals(mimeType)) f.mkdir();
            else f.createNewFile();
        } catch (java.io.IOException e) {
            throw new IllegalStateException(e);
        }
        return idOf(f);
    }

    private static boolean deleteRecursively(File f) {
        File[] kids = f.listFiles();
        if (kids != null) for (File k : kids) deleteRecursively(k);
        return f.delete();
    }

    @Override
    public void deleteDocument(String documentId) throws FileNotFoundException {
        if (!deleteRecursively(file(documentId))) throw new IllegalStateException("delete failed");
    }

    @Override
    public String renameDocument(String documentId, String displayName) throws FileNotFoundException {
        File f = file(documentId);
        File dst = new File(f.getParentFile(), displayName);
        if (dst.exists() || !f.renameTo(dst)) throw new IllegalStateException("rename failed");
        return idOf(dst);
    }

    @Override
    public String moveDocument(String sourceDocumentId, String sourceParentDocumentId, String targetParentDocumentId)
            throws FileNotFoundException {
        if (!supportsMove()) throw new UnsupportedOperationException("move");
        File f = file(sourceDocumentId);
        File dst = new File(file(targetParentDocumentId), f.getName());
        if (dst.exists() || !f.renameTo(dst)) throw new IllegalStateException("move failed");
        return idOf(dst);
    }

    @Override
    public boolean isChildDocument(String parentDocumentId, String documentId) {
        return documentId.equals(parentDocumentId) || documentId.startsWith(parentDocumentId + "/");
    }
}

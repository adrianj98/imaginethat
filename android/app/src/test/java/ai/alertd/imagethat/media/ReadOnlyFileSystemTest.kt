package ai.alertd.imagethat.media

import coil3.decode.ImageSource
import okio.Buffer
import okio.IOException
import okio.Path.Companion.toOkioPath
import okio.buffer
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.fail
import org.junit.Test
import java.io.File
import java.nio.file.Files

class ReadOnlyFileSystemTest {
    private val tmp = Files.createTempDirectory("rofs").toFile()

    private fun assertRefused(what: String, block: () -> Unit) {
        try {
            block()
            fail("$what was allowed")
        } catch (e: IOException) {
            assertEquals(true, e.message!!.contains("must not be written"))
        }
    }

    @Test fun everyWriteIsRefused() {
        val p = File(tmp, "x").toOkioPath()
        val existing = File(tmp, "e").apply { writeText("e") }.toOkioPath()
        assertRefused("sink") { ReadOnlyFileSystem.sink(p) }
        assertRefused("write") { ReadOnlyFileSystem.write(p) { writeUtf8("plain") } }
        assertRefused("appendingSink") { ReadOnlyFileSystem.appendingSink(existing) }
        assertRefused("openReadWrite") { ReadOnlyFileSystem.openReadWrite(p) }
        assertRefused("createDirectory") { ReadOnlyFileSystem.createDirectory(File(tmp, "d").toOkioPath()) }
        assertRefused("atomicMove") { ReadOnlyFileSystem.atomicMove(existing, p) }
        assertRefused("delete") { ReadOnlyFileSystem.delete(existing) }
        assertRefused("createSymlink") { ReadOnlyFileSystem.createSymlink(p, existing) }
        assertFalse(File(tmp, "x").exists())
        assertFalse(File(tmp, "d").exists())
    }

    @Test fun readsStillWork() {
        val f = File(tmp, "r").apply { writeBytes(byteArrayOf(1, 2, 3)) }
        assertArrayEquals(byteArrayOf(1, 2, 3), ReadOnlyFileSystem.source(f.toOkioPath()).buffer().use { it.readByteArray() })
    }

    /** What a decoder wanting a file path would do: Coil spills the buffer to a temp file. */
    @Test fun imageSourceCannotSpillToATempFile() {
        val before = tmp.list()!!.size
        val src = ImageSource(Buffer().write(byteArrayOf(9, 9, 9)), ReadOnlyFileSystem)
        try {
            src.file()
            fail("ImageSource.file() wrote the bytes to disk")
        } catch (e: java.io.IOException) {
            // refused
        } finally {
            src.close()
        }
        assertEquals(before, tmp.list()!!.size)
    }
}

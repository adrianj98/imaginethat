package ai.alertd.imagethat.local;

/** A provider that does not support moveDocument, to exercise SafStorage's copy fallback. */
public class TestDocsNoMoveProvider extends TestDocsProvider {
    public static final String AUTHORITY_NO_MOVE = "ai.alertd.imagethat.test.docs.nomove";

    @Override
    protected boolean supportsMove() {
        return false;
    }

    @Override
    protected String authority() {
        return AUTHORITY_NO_MOVE;
    }
}

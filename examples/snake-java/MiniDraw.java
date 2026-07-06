// MiniDraw.java — a tiny, self-contained, double-buffered drawing surface.
//
// This is a ~1-class stand-in for the handful of drawing calls the worm game
// uses, so the example depends on nothing but the JDK (and the lobbylink
// client). It intentionally mirrors the shape of the StdDraw calls the original
// wildlifejava game made — setScale/clear/circle/filledCircle/text/mouseX/
// mouseY/show/pause — so porting the worm logic took only trivial edits, but it
// carries no third-party code: it is plain AWT/Swing, BSD-licensed like
// the rest of this repo.
//
// Conventions (matching StdDraw): the y axis grows *upward*, the world is a
// square coordinate range you pick with the constructor, drawing is buffered
// (issue draw calls, then show() flips them onscreen), and the mouse is polled.

import java.awt.BasicStroke;
import java.awt.Color;
import java.awt.Dimension;
import java.awt.Font;
import java.awt.FontMetrics;
import java.awt.Graphics;
import java.awt.Graphics2D;
import java.awt.RenderingHints;
import java.awt.event.MouseAdapter;
import java.awt.event.MouseEvent;
import java.awt.event.WindowAdapter;
import java.awt.event.WindowEvent;
import java.awt.image.BufferedImage;

import javax.swing.JFrame;
import javax.swing.JPanel;
import javax.swing.SwingUtilities;

public final class MiniDraw {
    private final int size;             // canvas edge, pixels (square)
    private final double min, max;      // world coordinate range, both axes
    private final BufferedImage img;    // offscreen buffer we draw into
    private final Graphics2D g;
    private final JPanel panel;
    private volatile int mpx, mpy;      // last known mouse position, pixels
    private volatile boolean open = true;

    /** Open a square window of {@code pixels}×{@code pixels} showing world
     *  coordinates [worldMin, worldMax] on both axes. */
    public MiniDraw(String title, int pixels, double worldMin, double worldMax) {
        this.size = pixels;
        this.min = worldMin;
        this.max = worldMax;
        this.img = new BufferedImage(pixels, pixels, BufferedImage.TYPE_INT_RGB);
        this.g = img.createGraphics();
        g.setRenderingHint(RenderingHints.KEY_ANTIALIASING, RenderingHints.VALUE_ANTIALIAS_ON);
        g.setStroke(new BasicStroke(1f));   // hairline, like the original worm
        g.setFont(new Font("SansSerif", Font.BOLD, 14));
        clear(Color.BLACK);

        this.panel = new JPanel() {
            @Override protected void paintComponent(Graphics gg) {
                super.paintComponent(gg);
                gg.drawImage(img, 0, 0, getWidth(), getHeight(), null);
            }
        };
        panel.setPreferredSize(new Dimension(pixels, pixels));
        MouseAdapter mouse = new MouseAdapter() {
            @Override public void mouseMoved(MouseEvent e)   { mpx = e.getX(); mpy = e.getY(); }
            @Override public void mouseDragged(MouseEvent e) { mpx = e.getX(); mpy = e.getY(); }
        };
        panel.addMouseListener(mouse);
        panel.addMouseMotionListener(mouse);

        try {
            SwingUtilities.invokeAndWait(() -> {
                JFrame frame = new JFrame(title);
                frame.setDefaultCloseOperation(JFrame.DISPOSE_ON_CLOSE);
                frame.addWindowListener(new WindowAdapter() {
                    @Override public void windowClosing(WindowEvent e) { open = false; }
                    @Override public void windowClosed(WindowEvent e)  { open = false; }
                });
                frame.setContentPane(panel);
                frame.setResizable(false);
                frame.pack();
                frame.setLocationRelativeTo(null);
                frame.setVisible(true);
            });
        } catch (Exception e) {
            throw new RuntimeException("could not open window", e);
        }
    }

    /** False once the window has been closed. */
    public boolean isOpen() { return open; }

    // --- world -> pixel transforms (y flipped so world-y grows upward) --------
    private int px(double x) { return (int) Math.round((x - min) / (max - min) * size); }
    private int py(double y) { return (int) Math.round(size - (y - min) / (max - min) * size); }
    private int pr(double r) { return (int) Math.round(r / (max - min) * size); }

    /** Current mouse position in world coordinates. */
    public double mouseX() { return min + (mpx / (double) size) * (max - min); }
    public double mouseY() { return min + ((size - mpy) / (double) size) * (max - min); }

    public void clear(Color c)         { g.setColor(c); g.fillRect(0, 0, size, size); }
    public void setPenColor(Color c)   { g.setColor(c); }

    public void filledCircle(double x, double y, double r) {
        int rp = pr(r);
        g.fillOval(px(x) - rp, py(y) - rp, 2 * rp, 2 * rp);
    }
    public void circle(double x, double y, double r) {
        int rp = pr(r);
        g.drawOval(px(x) - rp, py(y) - rp, 2 * rp, 2 * rp);
    }

    /** Draw text left-aligned with its baseline at world (x, y). */
    public void textLeft(double x, double y, String s) { g.drawString(s, px(x), py(y)); }

    /** Draw text centered on world (x, y). */
    public void text(double x, double y, String s) {
        FontMetrics fm = g.getFontMetrics();
        g.drawString(s, px(x) - fm.stringWidth(s) / 2, py(y) + fm.getAscent() / 2);
    }

    /** Flip the offscreen buffer onto the screen. */
    public void show() { panel.repaint(); }

    /** Sleep for {@code ms} milliseconds (frame pacing). */
    public void pause(int ms) {
        try { Thread.sleep(ms); }
        catch (InterruptedException e) { Thread.currentThread().interrupt(); }
    }
}

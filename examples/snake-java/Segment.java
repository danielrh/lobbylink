public class Segment {
  public double radius;
  public double x;
  public double y;
  public double size;
  public Segment(double x, double y, double size){
    this.x = x;
    this.y = y;
    this.size = size;
    this.radius = 2;
  }
  public void attach(Segment segment){
    if (this.x == segment.x && this.y == segment.y) {
      this.x += 1;
    }
    double distance = this.dist(segment);
    this.x=(segment.x-this.x)*(distance-segment.radius)/distance+this.x;
    this.y=(segment.y-this.y)*(distance-segment.radius)/distance+this.y;
  }
  public double dist(Segment segment){
    return Math.sqrt(Math.abs(this.x-segment.x)*Math.abs(this.x-segment.x)+Math.abs(this.y-segment.y)*Math.abs(this.y-segment.y));
  }
  public static void main(String[] args){
    // Renderer swapped from Princeton's GPLv3 StdDraw to the small BSD MiniDraw;
    // the worm is otherwise unchanged. setCanvasSize + enableDoubleBuffering +
    // setScale fold into the MiniDraw constructor, and the once-infinite loop now
    // ends when the window is closed.
    MiniDraw md = new MiniDraw("wildlife — worm", 1000, -300, 300);
    Segment[] segments = new Segment[3000];
    for(int i = 0; i < segments.length; i += 1){
      if(i == 1 || i == 2){
        segments[i] = new Segment(0, 0, 4);
      }else{
        segments[i] = new Segment(0, 0, ((double)(segments.length-i))/1000);
      }
    }
    while(md.isOpen()){
      md.clear(java.awt.Color.WHITE);
      md.setPenColor(java.awt.Color.BLACK);
      segments[0].x = md.mouseX();
      segments[0].y = md.mouseY();
      for(int j = 1; j < segments.length; j += 1){
        segments[j].attach(segments[j-1]);
      }
      for(int j = 0; j < segments.length; j += 1){
        md.circle(segments[j].x,segments[j].y,segments[j].size);
      }
      md.show();
      md.pause(10);
    }
  }
}
